package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"runtime"
	"strings"
	"sync"

	"poisson/internal/auth"
	"poisson/internal/config"
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
	authMu   sync.Mutex
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
	return []Model{{ID: "gpt-5.5", Name: "gpt-5.5", ContextWindow: 400000}}, nil
}

// Stream sends a request to the Codex Responses API and returns a channel of
// StreamEvents. Refreshes the OAuth token when near expiry and retries once on
// a 401.
func (p *OpenAIProvider) Stream(ctx context.Context, req *Request) (<-chan StreamEvent, error) {
	return p.streamWithRetry(ctx, req, 0)
}

func (p *OpenAIProvider) streamWithRetry(ctx context.Context, req *Request, retry int) (<-chan StreamEvent, error) {
	p.authMu.Lock()
	entry, ok := p.auth["openai"]
	if ok && entry.Type == "oauth" && auth.IsExpired(entry, 5*60*1000) {
		if refreshed, err := auth.RefreshOpenAIToken(entry.Refresh); err == nil {
			p.auth["openai"] = *refreshed
			if serr := auth.Save(p.auth); serr != nil {
				log.Printf("warning: save openai auth after refresh: %v", serr)
			}
			entry = *refreshed
		}
	}
	p.authMu.Unlock()
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
		p.authMu.Lock()
		refreshed, rerr := auth.RefreshOpenAIToken(entry.Refresh)
		if rerr == nil {
			p.auth["openai"] = *refreshed
			if serr := auth.Save(p.auth); serr != nil {
				log.Printf("warning: save openai auth after refresh: %v", serr)
			}
		}
		p.authMu.Unlock()
		if rerr == nil {
			return p.streamWithRetry(ctx, req, 1)
		}
		return nil, fmt.Errorf("token expired, refresh failed: %w", rerr)
	}

	if resp.StatusCode != 200 {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
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
}

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
	Output    string           `json:"output,omitempty"`
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
	if effort := mapOpenAIEffort(req.Effort); effort != "" {
		body.Reasoning = &openaiReasoning{Effort: effort, Summary: "auto"}
	}

	for _, msg := range req.Messages {
		switch msg.Role {
		case "tool":
			for _, cb := range msg.Content {
				if cb.Type == "tool_result" {
					body.Input = append(body.Input, openaiRespItem{
						Type:   "function_call_output",
						CallID: cb.ToolCallID,
						Output: cb.ToolResult,
					})
				}
			}
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
