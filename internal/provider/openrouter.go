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

// defaultOpenRouterBaseURL is used when cfg.OpenRouter.BaseURL is empty.
const defaultOpenRouterBaseURL = "https://openrouter.ai/api/v1"

// OpenRouterProvider implements the Provider interface for OpenRouter
// (https://openrouter.ai) — a single OpenAI-compatible chat/completions
// endpoint proxying 400+ models from many labs. Auth is a plain API key
// (Bearer), never OAuth — unlike xai/openai/anthropic there is no token
// refresh path here.
type OpenRouterProvider struct {
	auth    auth.AuthStore
	config  *config.Config
	client  *http.Client
	baseURL string
}

// NewOpenRouterProvider creates an OpenRouter provider with the given auth
// and config. baseURL defaults to defaultOpenRouterBaseURL when
// cfg.OpenRouter.BaseURL is empty.
func NewOpenRouterProvider(a auth.AuthStore, cfg *config.Config) *OpenRouterProvider {
	baseURL := defaultOpenRouterBaseURL
	if cfg != nil && cfg.OpenRouter.BaseURL != "" {
		baseURL = cfg.OpenRouter.BaseURL
	}
	return &OpenRouterProvider{
		auth:    a,
		config:  cfg,
		client:  &http.Client{},
		baseURL: strings.TrimRight(baseURL, "/"),
	}
}

// ID returns "openrouter".
func (p *OpenRouterProvider) ID() string { return "openrouter" }

// Models returns the curated OpenRouter models (see KnownModels in
// models.go — the single source of truth for IDs/context windows).
func (p *OpenRouterProvider) Models() ([]Model, error) {
	if m := CuratedModels("openrouter"); len(m) > 0 {
		return m, nil
	}
	return []Model{{ID: "deepseek/deepseek-v4-flash-0731", Name: "deepseek/deepseek-v4-flash-0731", ContextWindow: 1048576}}, nil
}

// apiKey resolves the OpenRouter API key: auth.json (type api_key, set via
// `px login openrouter`) first, config.toml's openrouter.api_key second —
// same precedence Anthropic's non-OAuth path uses.
func (p *OpenRouterProvider) apiKey() string {
	if k := auth.GetAPIKey(p.auth, "openrouter"); k != "" {
		return k
	}
	if p.config != nil {
		return p.config.OpenRouter.APIKey
	}
	return ""
}

// Stream sends a request to OpenRouter's OpenAI-compatible chat/completions
// endpoint and returns a channel of StreamEvents.
func (p *OpenRouterProvider) Stream(ctx context.Context, req *Request) (<-chan StreamEvent, error) {
	key := p.apiKey()
	if key == "" {
		return nil, fmt.Errorf("no OpenRouter credentials — run: px login openrouter")
	}

	body := p.buildRequest(req)
	reqBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	// DoWithRetry retries connection failures and transient server errors
	// (429/5xx) with exponential backoff before this function returns. There
	// is no OAuth refresh here (plain API key), so unlike xai.go/openai.go a
	// 401 is simply a final, non-retryable error — a bad or revoked key.
	resp, err := DoWithRetry(ctx, DefaultRetryableStatus, func(attemptCtx context.Context) (*http.Response, error) {
		httpReq, err := http.NewRequestWithContext(attemptCtx, "POST",
			p.baseURL+"/chat/completions", bytes.NewReader(reqBody))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		httpReq.Header.Set("Authorization", "Bearer "+key)
		return p.client.Do(httpReq)
	})
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != 200 {
		respBody, _ := readCappedBody(resp)
		resp.Body.Close()
		return nil, fmt.Errorf("OpenRouter API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	ch := make(chan StreamEvent, 64)
	go pumpOpenAIChatCompletionsSSE(ctx, resp.Body, ch, openaiSSEConfig{
		ConvertUsage: func(u *openaiSSEUsage, _ int) *Usage { return convertOpenRouterUsage(u) },
		// Same as xai.go/ollama.go's identical pump call: without these, a
		// malformed chunk is silently skipped instead of surfaced, and a
		// connection that closes cleanly without ever sending a "[DONE]"
		// line closes the channel with no terminal event at all.
		FailOnParseError: true,
		EnsureDoneOnEOF:  true,
		ErrPrefix:        "OpenRouter",
	})
	return ch, nil
}

// openrouterRequest is the OpenAI-compatible chat/completions request body.
type openrouterRequest struct {
	Model           string               `json:"model"`
	Messages        []openrouterMessage  `json:"messages"`
	Tools           []openrouterTool     `json:"tools,omitempty"`
	MaxTokens       int                  `json:"max_tokens,omitempty"`
	Stream          bool                 `json:"stream"`
	StreamOptions   *openrouterStreamOpt `json:"stream_options,omitempty"`
	Temperature     *float64             `json:"temperature,omitempty"`
	ReasoningEffort string               `json:"reasoning_effort,omitempty"`
}

type openrouterStreamOpt struct {
	IncludeUsage bool `json:"include_usage"`
}

type openrouterMessage struct {
	Role       string               `json:"role"`
	Content    any                  `json:"content"` // string or []oaiContentPart (multimodal)
	ToolCalls  []openrouterToolCall `json:"tool_calls,omitempty"`
	ToolCallID string               `json:"tool_call_id,omitempty"`
}

type openrouterToolCall struct {
	ID       string                 `json:"id"`
	Type     string                 `json:"type"`
	Function openrouterToolFunction `json:"function"`
}

type openrouterToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openrouterTool struct {
	Type     string            `json:"type"`
	Function openrouterToolDef `json:"function"`
}

type openrouterToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// buildRequest converts a Poisson Request to OpenRouter's OpenAI-compatible
// format. Every message carries a content field (even if empty) — the same
// requirement xai.go/ollama.go already document for OpenAI-compatible
// chat/completions servers.
func (p *OpenRouterProvider) buildRequest(req *Request) openrouterRequest {
	or := openrouterRequest{
		Model:           req.Model,
		Stream:          true,
		StreamOptions:   &openrouterStreamOpt{IncludeUsage: true},
		MaxTokens:       req.MaxTokens,
		Temperature:     req.Temperature,
		ReasoningEffort: req.Effort,
	}

	for _, sb := range req.System {
		or.Messages = append(or.Messages, openrouterMessage{Role: "system", Content: sb.Text})
	}

	for _, msg := range req.Messages {
		if msg.Role == "tool" {
			or.Messages = append(or.Messages, openrouterToolResultMessages(msg.Content)...)
			continue
		}

		var textParts []string
		var toolCalls []openrouterToolCall
		var images []ContentBlock

		for _, cb := range msg.Content {
			switch cb.Type {
			case "text":
				textParts = append(textParts, cb.Text)
			case "image":
				images = append(images, cb)
			case "tool_use":
				toolCalls = append(toolCalls, openrouterToolCall{
					ID:   cb.ToolCallID,
					Type: "function",
					Function: openrouterToolFunction{
						Name:      cb.ToolName,
						Arguments: string(cb.ToolInput),
					},
				})
			case "tool_result":
				// See xaiToolResultMessages' identical comment: /btw folds a
				// placeholder tool_result into the same user turn as its
				// question, but this flat role-tagged format has no
				// alternation constraint, so it's emitted as its own
				// role:"tool" message ahead of the user message below.
				or.Messages = append(or.Messages, openrouterMessage{
					Role:       "tool",
					Content:    cb.ToolResult,
					ToolCallID: cb.ToolCallID,
				})
			}
		}

		om := openrouterMessage{Role: msg.Role}
		text := strings.Join(textParts, "\n")
		if len(images) > 0 {
			om.Content = openAIUserContent(text, images)
		} else if len(textParts) > 0 {
			om.Content = text
		} else {
			om.Content = "" // content must always be set, even for tool-only turns
		}
		if len(toolCalls) > 0 {
			om.ToolCalls = toolCalls
		}
		or.Messages = append(or.Messages, om)
	}

	for _, td := range req.Tools {
		or.Tools = append(or.Tools, openrouterTool{
			Type: "function",
			Function: openrouterToolDef{
				Name:        td.Name,
				Description: td.Description,
				Parameters:  td.Schema,
			},
		})
	}

	return or
}

// openrouterToolResultMessages mirrors xaiToolResultMessages: converts a
// genuine "tool"-role message's content blocks into chat messages, pairing
// a tool_result with a following sibling "image" block (see ContentBlock's
// ImagePath doc comment) as an ordinary user-role image message.
func openrouterToolResultMessages(blocks []ContentBlock) []openrouterMessage {
	var out []openrouterMessage
	pendingImage := false
	for _, cb := range blocks {
		switch cb.Type {
		case "tool_result":
			out = append(out, openrouterMessage{Role: "tool", Content: cb.ToolResult, ToolCallID: cb.ToolCallID})
			pendingImage = true
		case "image":
			if !pendingImage {
				continue
			}
			out = append(out, openrouterMessage{Role: "user", Content: openAIUserContent("", []ContentBlock{cb})})
			pendingImage = false
		}
	}
	return out
}

// convertOpenRouterUsage maps OpenRouter's OpenAI-compatible usage object.
// prompt_tokens_details.cached_tokens is a prompt-cache hit count (same
// shape xAI/OpenAI report); InputTokens excludes it, reported separately as
// CacheReadTokens — matches convertXAIUsage's convention.
func convertOpenRouterUsage(u *openaiSSEUsage) *Usage {
	if u == nil {
		return nil
	}
	cacheRead := u.PromptTokensDetails.CachedTokens
	input := u.PromptTokens - cacheRead
	if input < 0 {
		input = 0
	}
	output := u.CompletionTokens + u.CompletionTokensDetails.ReasoningTokens
	return &Usage{InputTokens: input, CacheReadTokens: cacheRead, OutputTokens: output}
}
