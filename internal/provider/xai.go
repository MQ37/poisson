package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/mq37/poisson/internal/auth"
	"github.com/mq37/poisson/internal/config"
)

// XAIProvider implements the Provider interface for xAI's Grok API
// (OpenAI-compatible endpoint).
type XAIProvider struct {
	auth   auth.AuthStore
	config *config.Config
	client *http.Client
}

// NewXAIProvider creates an xAI provider with the given auth and config.
func NewXAIProvider(a auth.AuthStore, cfg *config.Config) *XAIProvider {
	return &XAIProvider{
		auth:   a,
		config: cfg,
		client: &http.Client{},
	}
}

// ID returns "xai".
func (p *XAIProvider) ID() string { return "xai" }

// Models returns the curated xAI models (see KnownModels in models.go — the
// single source of truth for IDs/context windows, so this list can't drift
// out of sync with it).
func (p *XAIProvider) Models() ([]Model, error) {
	if m := CuratedModels("xai"); len(m) > 0 {
		return m, nil
	}
	return []Model{
		{ID: "grok-build", Name: "Grok Build", ContextWindow: 256000},
		{ID: "grok-4.5", Name: "Grok 4.5", ContextWindow: 500000},
	}, nil
}

// Stream sends a request to the xAI API (OpenAI-compatible) and returns
// a channel of StreamEvents. Uses OAuth Bearer token authentication with
// auto-refresh on 401.
func (p *XAIProvider) Stream(ctx context.Context, req *Request) (<-chan StreamEvent, error) {
	return p.streamWithRetry(ctx, req, 0)
}

func (p *XAIProvider) streamWithRetry(ctx context.Context, req *Request, retry int) (<-chan StreamEvent, error) {
	// EnsureXAIFresh guards the shared "xai" AuthStore entry with a
	// package-level lock (poisson/internal/auth) — WebAskTool's grok backend
	// holds a reference to this same map and must not race writing it.
	entry, err := auth.EnsureXAIFresh(p.auth, 5*60*1000)
	if err != nil {
		return nil, err
	}

	// Build OpenAI-compatible request body.
	xaiReq := p.buildRequest(req)
	reqBody, err := json.Marshal(xaiReq)
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
		httpReq, err := http.NewRequestWithContext(attemptCtx, "POST",
			"https://api.x.ai/v1/chat/completions", bytes.NewReader(reqBody))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		httpReq.Header.Set("Authorization", "Bearer "+entry.Access)
		return p.client.Do(httpReq)
	})
	if err != nil {
		return nil, err
	}

	// Handle 401 with refresh + retry.
	if resp.StatusCode == 401 && retry == 0 {
		resp.Body.Close()
		if _, err := auth.ForceRefreshXAI(p.auth); err != nil {
			return nil, fmt.Errorf("token expired, refresh failed: %w", err)
		}
		return p.streamWithRetry(ctx, req, 1)
	}

	if resp.StatusCode != 200 {
		body, _ := readCappedBody(resp)
		resp.Body.Close()
		return nil, fmt.Errorf("xAI API error (status %d): %s", resp.StatusCode, string(body))
	}

	ch := make(chan StreamEvent, 64)
	go pumpOpenAIChatCompletionsSSE(ctx, resp.Body, ch, openaiSSEConfig{
		ConvertUsage: func(u *openaiSSEUsage, _ int) *Usage {
			return convertXAIUsage(u)
		},
		// Same as ollama.go's identical pump call: without these, a malformed
		// chunk is silently skipped instead of surfaced, and a connection that
		// closes cleanly without ever sending a "[DONE]" line closes the
		// channel with no terminal event at all instead of an EventDone.
		FailOnParseError: true,
		EnsureDoneOnEOF:  true,
		ErrPrefix:        "xAI",
	})
	return ch, nil
}

// xaiRequest is the OpenAI-compatible request body.
type xaiRequest struct {
	Model           string            `json:"model"`
	Messages        []xaiMessage      `json:"messages"`
	Tools           []xaiTool         `json:"tools,omitempty"`
	MaxTokens       int               `json:"max_tokens,omitempty"`
	Stream          bool              `json:"stream"`
	StreamOptions   *xaiStreamOptions `json:"stream_options,omitempty"`
	Temperature     *float64          `json:"temperature,omitempty"`
	ReasoningEffort string            `json:"reasoning_effort,omitempty"`
}

type xaiStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type xaiMessage struct {
	Role       string        `json:"role"`
	Content    any           `json:"content"` // string or []oaiContentPart (multimodal)
	ToolCalls  []xaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
}

type xaiToolCall struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Function xaiToolFunction `json:"function"`
}

type xaiToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type xaiTool struct {
	Type     string     `json:"type"`
	Function xaiToolDef `json:"function"`
}

type xaiToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// buildRequest converts a Poisson Request to the xAI OpenAI-compatible format.
// Every message MUST have a content field (even if empty) or xAI returns 422.
func (p *XAIProvider) buildRequest(req *Request) xaiRequest {
	ar := xaiRequest{
		Model:           req.Model,
		Stream:          true,
		StreamOptions:   &xaiStreamOptions{IncludeUsage: true},
		MaxTokens:       req.MaxTokens,
		Temperature:     req.Temperature,
		ReasoningEffort: req.Effort,
	}

	// System blocks → system messages.
	for _, sb := range req.System {
		ar.Messages = append(ar.Messages, xaiMessage{
			Role:    "system",
			Content: sb.Text,
		})
	}

	// Messages.
	for _, msg := range req.Messages {
		if msg.Role == "tool" {
			ar.Messages = append(ar.Messages, xaiToolResultMessages(msg.Content)...)
			continue
		}

		var textParts []string
		var toolCalls []xaiToolCall
		var images []ContentBlock

		for _, cb := range msg.Content {
			switch cb.Type {
			case "text":
				textParts = append(textParts, cb.Text)
			case "image":
				images = append(images, cb)
			case "tool_use":
				toolCalls = append(toolCalls, xaiToolCall{
					ID:   cb.ToolCallID,
					Type: "function",
					Function: xaiToolFunction{
						Name:      cb.ToolName,
						Arguments: string(cb.ToolInput),
					},
				})
			case "tool_result":
				// A user turn is normally plain text/image, but /btw folds a
				// placeholder tool_result for a still-running tool call (see
				// quickanswer.go's pendingToolResultBlocks) into the same
				// turn as its question, rather than a separate "tool"-role
				// message — that's an Anthropic-specific constraint (it
				// rejects two consecutive user-role messages). xAI's chat
				// messages are a flat, role-tagged list with no such
				// alternation rule, so this is emitted as its own ordinary
				// role:"tool" message ahead of the user message below.
				ar.Messages = append(ar.Messages, xaiMessage{
					Role:       "tool",
					Content:    cb.ToolResult,
					ToolCallID: cb.ToolCallID,
				})
			}
		}

		// Build the message — content must always be set (not nil).
		xm := xaiMessage{Role: msg.Role}
		text := strings.Join(textParts, "\n")
		if len(images) > 0 {
			xm.Content = openAIUserContent(text, images)
		} else if len(textParts) > 0 {
			xm.Content = text
		} else {
			xm.Content = "" // content must always be set (not nil), even for tool-only turns
		}
		if len(toolCalls) > 0 {
			xm.ToolCalls = toolCalls
		}
		ar.Messages = append(ar.Messages, xm)
	}

	// Tools.
	for _, td := range req.Tools {
		ar.Tools = append(ar.Tools, xaiTool{
			Type: "function",
			Function: xaiToolDef{
				Name:        td.Name,
				Description: td.Description,
				Parameters:  td.Schema,
			},
		})
	}

	return ar
}

// xaiToolResultMessages converts a genuine "tool"-role message's content
// blocks into OpenAI-compatible chat messages: one role:"tool" message per
// tool_result, plus a following role:"user" message carrying the image when
// that tool_result has a sibling "image" block (a tool that loaded an image
// for the model, currently only `read` on an image file — see ContentBlock's
// ImagePath doc comment). Tool-role image content isn't reliably supported
// across chat-completions-compatible servers, but an ordinary user-role
// image message always is, and this format has no role-alternation
// constraint (unlike Anthropic), so inserting one costs nothing.
func xaiToolResultMessages(blocks []ContentBlock) []xaiMessage {
	var out []xaiMessage
	pendingImage := false
	for _, cb := range blocks {
		switch cb.Type {
		case "tool_result":
			out = append(out, xaiMessage{
				Role: "tool", Content: cb.ToolResult, ToolCallID: cb.ToolCallID,
			})
			pendingImage = true
		case "image":
			if !pendingImage {
				continue
			}
			out = append(out, xaiMessage{Role: "user", Content: openAIUserContent("", []ContentBlock{cb})})
			pendingImage = false
		}
	}
	return out
}

func convertXAIUsage(u *openaiSSEUsage) *Usage {
	if u == nil {
		return nil
	}
	// xAI reports reasoning_tokens disjoint from completion_tokens (they sum to
	// total-prompt), so add them. total-prompt is a floor for when either is missing.
	output := u.CompletionTokens + u.CompletionTokensDetails.ReasoningTokens
	if totalOutput := u.TotalTokens - u.PromptTokens; totalOutput > output {
		output = totalOutput
	}
	// prompt_tokens_details.cached_tokens was previously never read here, so
	// cache hits were always counted as full-price input — matches the
	// convention convertOpenAIRespUsage already uses (InputTokens excludes
	// the cached portion, reported separately as CacheReadTokens).
	cacheRead := u.PromptTokensDetails.CachedTokens
	input := u.PromptTokens - cacheRead
	if input < 0 {
		input = 0
	}
	return &Usage{InputTokens: input, CacheReadTokens: cacheRead, OutputTokens: output}
}
