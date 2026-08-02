package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"runtime"
	"strings"
	"sync"

	"github.com/mq37/poisson/internal/auth"
	"github.com/mq37/poisson/internal/config"
)

// codexResponsesURL is the ChatGPT subscription Responses endpoint. The Codex
// subscription only works through this backend (not api.openai.com), authed
// with a ChatGPT OAuth access token + chatgpt-account-id.
const codexResponsesURL = "https://chatgpt.com/backend-api/codex/responses"

// jwtAccountClaim is the JWT claim namespace holding chatgpt_account_id.
const jwtAccountClaim = "https://api.openai.com/auth"

// OpenAIProvider implements the Provider interface for OpenAI's GPT models via
// the ChatGPT Codex subscription (Responses API + SSE). It uses OAuth Bearer
// auth with auto-refresh, mirroring the xAI/Anthropic subscription flows.
type OpenAIProvider struct {
	auth     auth.AuthStore
	config   *config.Config
	client   *http.Client
	endpoint string // Codex Responses URL; overridable in tests

	webBaseURL string // chatgpt.com web/session backend (usage, reset credits); overridable in tests
	usageMu    sync.Mutex
	usageCache *CodexUsage // see openai_usage.go
}

// NewOpenAIProvider creates an OpenAI provider with the given auth and config.
func NewOpenAIProvider(a auth.AuthStore, cfg *config.Config) *OpenAIProvider {
	return &OpenAIProvider{auth: a, config: cfg, client: &http.Client{}, endpoint: codexResponsesURL, webBaseURL: codexWebBaseURL}
}

// ID returns "openai".
func (p *OpenAIProvider) ID() string { return "openai" }

// Models returns the curated OpenAI models.
func (p *OpenAIProvider) Models() ([]Model, error) {
	if m := CuratedModels("openai"); len(m) > 0 {
		return m, nil
	}
	return []Model{{ID: "gpt-5.6-terra", Name: "gpt-5.6-terra", ContextWindow: 272000}}, nil
}

// Stream sends a request to the Codex Responses API and returns a channel of
// StreamEvents. Refreshes the OAuth token when near expiry and retries once on
// a 401.
func (p *OpenAIProvider) Stream(ctx context.Context, req *Request) (<-chan StreamEvent, error) {
	return p.streamWithRetry(ctx, req, 0)
}

func (p *OpenAIProvider) streamWithRetry(ctx context.Context, req *Request, retry int) (<-chan StreamEvent, error) {
	// auth.StoreMu, not a private-to-this-struct mutex: the same underlying
	// AuthStore map is also written by XAIProvider/WebAskTool's grok backend
	// (see auth.StoreMu's doc comment for why a per-provider-instance mutex
	// isn't enough to prevent a concurrent map write). The refresh itself
	// goes through auth.RefreshIfExpired, cross-process safe too — see its
	// doc comment.
	auth.StoreMu.Lock()
	entry, ok := p.auth["openai"]
	if ok && entry.Type == "oauth" {
		if refreshed, err := auth.RefreshIfExpired(p.auth, "openai", 5*60*1000, auth.RefreshOpenAIToken); err == nil {
			entry = refreshed
		} else {
			log.Printf("warning: refresh openai auth: %v", err)
		}
	}
	auth.StoreMu.Unlock()
	if !ok || entry.Type != "oauth" {
		return nil, fmt.Errorf("no OpenAI credentials — run: px login openai")
	}

	accountID, err := extractAccountID(entry.Access)
	if err != nil {
		return nil, err
	}

	reqBody, err := json.Marshal(p.buildRequest(req))
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	// DoWithRetry retries connection failures and transient server errors
	// (429/5xx) with exponential backoff, indefinitely, before this function
	// returns — the 401 refresh-and-retry-once logic below only ever sees the
	// final response DoWithRetry settles on. A fresh request is built on
	// every attempt so a body already consumed by a failed attempt is never
	// resent short or empty.
	resp, err := DoWithRetry(ctx, DefaultRetryableStatus, func(attemptCtx context.Context) (*http.Response, error) {
		httpReq, err := http.NewRequestWithContext(attemptCtx, "POST", p.endpoint, bytes.NewReader(reqBody))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		httpReq.Header.Set("Authorization", "Bearer "+entry.Access)
		httpReq.Header.Set("chatgpt-account-id", accountID)
		httpReq.Header.Set("OpenAI-Beta", "responses=experimental")
		httpReq.Header.Set("originator", "poisson")
		httpReq.Header.Set("User-Agent", fmt.Sprintf("poisson (%s %s)", runtime.GOOS, runtime.GOARCH))
		return p.client.Do(httpReq)
	})
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == 401 && retry == 0 {
		resp.Body.Close()
		auth.StoreMu.Lock()
		_, rerr := auth.ForceRefresh(p.auth, "openai", 0, auth.RefreshOpenAIToken)
		auth.StoreMu.Unlock()
		if rerr == nil {
			return p.streamWithRetry(ctx, req, 1)
		}
		return nil, fmt.Errorf("token expired, refresh failed: %w", rerr)
	}

	if resp.StatusCode != 200 {
		body, readErr := readCappedBody(resp)
		resp.Body.Close()
		detail := strings.TrimSpace(string(body))
		if detail == "" {
			detail = http.StatusText(resp.StatusCode)
		}
		if detail == "" && readErr != nil {
			detail = readErr.Error()
		}
		if detail == "" {
			detail = "provider returned no details"
		}
		return nil, fmt.Errorf("OpenAI API error (status %d): %s", resp.StatusCode, detail)
	}

	ch := make(chan StreamEvent, 64)
	go pumpOpenAIResponsesSSE(ctx, resp.Body, ch)
	return ch, nil
}

// openaiRespRequest is the Codex Responses API request body.
type openaiRespRequest struct {
	Model             string           `json:"model"`
	Store             bool             `json:"store"`
	Stream            bool             `json:"stream"`
	Instructions      string           `json:"instructions,omitempty"`
	Input             []openaiRespItem `json:"input"`
	Tools             []openaiRespTool `json:"tools,omitempty"`
	ToolChoice        string           `json:"tool_choice,omitempty"`
	ParallelToolCalls bool             `json:"parallel_tool_calls"`
	Reasoning         *openaiReasoning `json:"reasoning,omitempty"`
	PromptCacheKey    string           `json:"prompt_cache_key,omitempty"`
	MaxOutputTokens   int              `json:"max_output_tokens,omitempty"`
}

// openaiMaxOutputTokens caps a turn's output when the caller leaves
// Request.MaxTokens unset (0) — mirrors anthropicMaxOutputTokens (same
// value) and defaultOllamaMaxTokens. Without this, every real Codex turn
// went out with no client-side ceiling at all: a misbehaving/looping
// reasoning run (effort up to xhigh/max) could run indefinitely, unlike its
// sibling providers which explicitly guard against exactly that.
const openaiMaxOutputTokens = 64000

// openaiPromptCacheKeyMax is the Responses API limit for prompt_cache_key.
const openaiPromptCacheKeyMax = 64

// clampPromptCacheKey caps the key at 64 runes (Responses API limit).
func clampPromptCacheKey(key string) string {
	r := []rune(key)
	if len(r) <= openaiPromptCacheKeyMax {
		return key
	}
	return string(r[:openaiPromptCacheKeyMax])
}

type openaiReasoning struct {
	Effort  string `json:"effort"`
	Summary string `json:"summary,omitempty"`
}

// openaiRespItem is one Responses `input` item. Only the fields relevant to the
// item's Type are populated (message | function_call | function_call_output).
type openaiRespItem struct {
	Type      string           `json:"type"`
	Role      string           `json:"role,omitempty"`
	Content   []openaiRespPart `json:"content,omitempty"`
	CallID    string           `json:"call_id,omitempty"`
	Name      string           `json:"name,omitempty"`
	Arguments string           `json:"arguments,omitempty"`
	// Output is pre-marshaled JSON (see openaiFunctionCallOutput), not
	// string+omitempty: the Responses API requires "output" present on
	// every function_call_output item, even when the tool legitimately
	// produced no content (e.g. `ls` on an empty dir, `read` on an empty
	// file) — a plain string+omitempty drops the field entirely for "",
	// and the API then rejects the request with "Missing required
	// parameter: 'input[N].output'". json.RawMessage's omitempty only
	// triggers on a genuinely empty/nil slice, so always marshaling a real
	// value (even `""`) keeps the field present. Output can be either a
	// plain JSON string or an array of input_text/input_image parts (a
	// tool that loaded an image, e.g. `read` on an image file, needs the
	// latter — see ContentBlock's ImagePath doc comment). Left nil
	// (omitted) for message/function_call items, where "output" doesn't
	// apply.
	Output json.RawMessage `json:"output,omitempty"`
}

// openaiFunctionCallOutput builds a function_call_output item's "output"
// value: a plain JSON string when image is nil, or an array of
// input_text/input_image parts when the tool result carries an image (the
// Responses API accepts both shapes for function_call_output.output).
// Always returns non-empty bytes, even for text == "" — see Output's doc
// comment for why that matters.
func openaiFunctionCallOutput(text string, image *ContentBlock) json.RawMessage {
	if image != nil {
		if url, ok := imageBlockDataURL(*image); ok {
			raw, err := json.Marshal([]openaiRespPart{
				{Type: "input_text", Text: text},
				{Type: "input_image", ImageURL: url},
			})
			if err == nil {
				return raw
			}
		}
	}
	raw, _ := json.Marshal(text)
	return raw
}

// openaiFunctionCallOutputItems converts tool_result blocks into
// function_call_output items, pairing each with an immediately-following
// sibling "image" block (see ContentBlock's ImagePath doc comment) into one
// item's Output. Adjacency is strict — ANY other block type in between
// (e.g. a real text/image block belonging to the user's own message, which
// shares this same blocks slice when /btw folds tool_result placeholders
// into a user turn) clears the pending tool_result, so only a genuine
// image sibling produced right after its own tool_result (agent.go's
// ordering) is ever paired — never a user's own attached image mistaken
// for one.
func openaiFunctionCallOutputItems(blocks []ContentBlock) []openaiRespItem {
	var items []openaiRespItem
	var pending *openaiRespItem
	var pendingText string
	flush := func() {
		if pending != nil {
			items = append(items, *pending)
			pending = nil
		}
	}
	for _, cb := range blocks {
		switch cb.Type {
		case "tool_result":
			flush()
			pendingText = cb.ToolResult
			pending = &openaiRespItem{
				Type:   "function_call_output",
				CallID: cb.ToolCallID,
				Output: openaiFunctionCallOutput(pendingText, nil),
			}
		case "image":
			if pending == nil {
				continue
			}
			img := cb
			pending.Output = openaiFunctionCallOutput(pendingText, &img)
			flush()
		default:
			flush()
		}
	}
	flush()
	return items
}

// openaiRespPart is one content part. Text parts use input_text/output_text;
// image parts use input_image with a data: URL string.
type openaiRespPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

type openaiRespTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// buildRequest converts a Poisson Request to the Codex Responses format.
// Reasoning items are never replayed (we don't request encrypted_content), so
// assistant turns carry only their text and function calls.
func (p *OpenAIProvider) buildRequest(req *Request) openaiRespRequest {
	var sys []string
	for _, sb := range req.System {
		if sb.Text != "" {
			sys = append(sys, sb.Text)
		}
	}

	body := openaiRespRequest{
		Model:             req.Model,
		Store:             false,
		Stream:            true,
		Instructions:      strings.Join(sys, "\n\n"),
		ToolChoice:        "auto",
		ParallelToolCalls: true,
		// prompt_cache_key routes repeated turns to the same prompt cache so the
		// large stable prefix (instructions + tools + conversation) is served
		// from cache instead of re-billed in full every turn. Codex forces
		// store:false, so full context is resent each turn — caching is the only
		// lever to cut the tokens counted against the usage limit.
		PromptCacheKey: clampPromptCacheKey(req.CacheKey),
	}
	body.MaxOutputTokens = req.MaxTokens
	if body.MaxOutputTokens == 0 {
		body.MaxOutputTokens = openaiMaxOutputTokens
	}
	if effort := mapOpenAIEffort(req.Effort); effort != "" {
		body.Reasoning = &openaiReasoning{Effort: effort, Summary: "auto"}
	}

	for _, msg := range req.Messages {
		switch msg.Role {
		case "tool":
			body.Input = append(body.Input, openaiFunctionCallOutputItems(msg.Content)...)
		case "assistant":
			var parts []openaiRespPart
			var calls []openaiRespItem
			for _, cb := range msg.Content {
				switch cb.Type {
				case "text":
					if cb.Text != "" {
						parts = append(parts, openaiRespPart{Type: "output_text", Text: cb.Text})
					}
				case "tool_use":
					calls = append(calls, openaiRespItem{
						Type:      "function_call",
						CallID:    cb.ToolCallID,
						Name:      cb.ToolName,
						Arguments: string(cb.ToolInput),
					})
				}
			}
			if len(parts) > 0 {
				body.Input = append(body.Input, openaiRespItem{Type: "message", Role: "assistant", Content: parts})
			}
			body.Input = append(body.Input, calls...)
		default: // user
			// A user turn is normally plain text/image, but /btw folds
			// placeholder tool_result blocks for a still-running tool call
			// (see quickanswer.go's pendingToolResultBlocks) into the same
			// turn as its question, rather than a separate "tool"-role
			// message — Anthropic rejects two consecutive user-role
			// messages. The Responses API has no such alternation
			// constraint (input is a flat, order-based item list), so
			// those blocks are just emitted as their own
			// function_call_output items ahead of the user message.
			body.Input = append(body.Input, openaiFunctionCallOutputItems(msg.Content)...)
			parts := openaiUserParts(msg.Content)
			if len(parts) > 0 {
				body.Input = append(body.Input, openaiRespItem{Type: "message", Role: "user", Content: parts})
			}
		}
	}

	for _, td := range req.Tools {
		body.Tools = append(body.Tools, openaiRespTool{
			Type:        "function",
			Name:        td.Name,
			Description: td.Description,
			Parameters:  td.Schema,
		})
	}

	return body
}

// openaiUserParts builds the content parts for a user message: input_text for
// text, input_image (data URL) for images. Unreadable images are skipped.
func openaiUserParts(blocks []ContentBlock) []openaiRespPart {
	var parts []openaiRespPart
	for _, cb := range blocks {
		switch cb.Type {
		case "text":
			if cb.Text != "" {
				parts = append(parts, openaiRespPart{Type: "input_text", Text: cb.Text})
			}
		case "image":
			if url, ok := imageBlockDataURL(cb); ok {
				parts = append(parts, openaiRespPart{Type: "input_image", ImageURL: url})
			}
		}
	}
	return parts
}

// mapOpenAIEffort maps Poisson effort levels to Responses reasoning.effort.
// gpt-5.5 tops out at "xhigh", so "max" maps there. "" means omit (server
// default: medium).
func mapOpenAIEffort(effort string) string {
	switch effort {
	case "":
		return ""
	case "max":
		return "xhigh"
	default:
		return effort
	}
}

// extractAccountID decodes the JWT access token and returns the
// chatgpt_account_id claim required by the Codex backend.
func extractAccountID(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("openai: malformed access token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("openai: decode token payload: %w", err)
	}
	var claims map[string]json.RawMessage
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("openai: parse token payload: %w", err)
	}
	var auth struct {
		AccountID string `json:"chatgpt_account_id"`
	}
	if raw, ok := claims[jwtAccountClaim]; ok {
		_ = json.Unmarshal(raw, &auth)
	}
	if auth.AccountID == "" {
		return "", fmt.Errorf("openai: no chatgpt_account_id in token — re-run: px login openai")
	}
	return auth.AccountID, nil
}
