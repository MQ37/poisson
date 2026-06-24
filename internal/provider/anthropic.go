package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"poisson/internal/auth"
	"poisson/internal/config"
)

// AnthropicProvider implements the Provider interface for Anthropic's
// Messages API. It supports both API key and OAuth authentication.
// When OAuth is active, stealth transformations are applied (see
// anthropic_stealth.go).
type AnthropicProvider struct {
	baseURL string
	auth    auth.AuthStore
	config  *config.Config
	client  *http.Client
}

// NewAnthropicProvider creates an Anthropic provider with the given auth
// store and config. The baseURL defaults to https://api.anthropic.com.
func NewAnthropicProvider(a auth.AuthStore, cfg *config.Config) *AnthropicProvider {
	baseURL := "https://api.anthropic.com"
	if cfg != nil && cfg.Anthropic.APIKey != "" {
		// Could have a custom baseURL in the future
	}
	return &AnthropicProvider{
		baseURL: baseURL,
		auth:    a,
		config:  cfg,
		client:  &http.Client{},
	}
}

// ID returns "anthropic".
func (p *AnthropicProvider) ID() string { return "anthropic" }

// Models returns the known Anthropic models with their context windows.
func (p *AnthropicProvider) Models() ([]Model, error) {
	return []Model{
		{ID: "claude-opus-4-8", Name: "Claude Opus 4.8", ContextWindow: 1000000},
	}, nil
}

// Stream sends a request to the Anthropic Messages API and returns a channel
// of StreamEvents. It handles both API key and OAuth authentication, and
// applies stealth transformations when OAuth is active.
func (p *AnthropicProvider) Stream(ctx context.Context, req *Request) (<-chan StreamEvent, error) {
	isOAuth := auth.IsOAuth(p.auth, "anthropic")

	// Check for token refresh if OAuth.
	if isOAuth {
		entry := p.auth["anthropic"]
		if auth.IsExpired(entry, 5*60*1000) {
			refreshed, err := auth.RefreshAnthropicToken(entry.Refresh)
			if err == nil {
				p.auth["anthropic"] = *refreshed
				_ = auth.Save(p.auth)
			}
		}
	}

	// Apply stealth if OAuth.
	if isOAuth {
		p.applyStealth(req)
	}

	// Build the HTTP request.
	anthropicReq := p.buildAnthropicRequest(req, isOAuth)
	reqBody, err := json.Marshal(anthropicReq)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		p.baseURL+"/v1/messages", bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}

	// Set headers.
	p.setHeaders(httpReq, isOAuth)

	// Send.
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("anthropic API error (status %d): %s", resp.StatusCode, string(body))
	}

	// Parse SSE stream.
	ch := make(chan StreamEvent, 64)
	go p.pumpSSE(ctx, resp.Body, ch)
	return ch, nil
}

// anthropicRequest is the JSON body sent to the Messages API.
type anthropicRequest struct {
	Model       string             `json:"model"`
	Messages    []anthropicMessage `json:"messages"`
	System      []anthropicSystem  `json:"system,omitempty"`
	Tools       []anthropicTool    `json:"tools,omitempty"`
	MaxTokens   int                `json:"max_tokens"`
	Stream      bool               `json:"stream"`
	Temperature *float64           `json:"temperature,omitempty"`
	Effort      string             `json:"effort,omitempty"`
}

type anthropicMessage struct {
	Role    string                  `json:"role"`
	Content []anthropicContentBlock `json:"content"`
}

type anthropicContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"` // for tool_result
}

type anthropicSystem struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	CacheCtl string `json:"cache_control,omitempty"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// buildAnthropicRequest converts a Poisson Request to the Anthropic API format.
func (p *AnthropicProvider) buildAnthropicRequest(req *Request, isOAuth bool) anthropicRequest {
	ar := anthropicRequest{
		Model:       req.Model,
		MaxTokens:   req.MaxTokens,
		Stream:      true,
		Temperature: req.Temperature,
		Effort:      req.Effort,
	}
	if ar.MaxTokens == 0 {
		ar.MaxTokens = 4096
	}

	// System blocks.
	for _, sb := range req.System {
		as := anthropicSystem{Type: "text", Text: sb.Text}
		if sb.CacheCtl != "" {
			as.CacheCtl = sb.CacheCtl
		}
		ar.System = append(ar.System, as)
	}

	// Messages.
	for _, msg := range req.Messages {
		am := anthropicMessage{Role: msg.Role}
		for _, cb := range msg.Content {
			switch cb.Type {
			case "text":
				am.Content = append(am.Content, anthropicContentBlock{
					Type: "text", Text: cb.Text,
				})
			case "tool_use":
				am.Content = append(am.Content, anthropicContentBlock{
					Type: "tool_use", ID: cb.ToolCallID, Name: cb.ToolName, Input: cb.ToolInput,
				})
			case "tool_result":
				resultContent, _ := json.Marshal(cb.ToolResult)
				am.Content = append(am.Content, anthropicContentBlock{
					Type: "tool_result", ToolUseID: cb.ToolCallID, Content: resultContent,
				})
			}
		}
		ar.Messages = append(ar.Messages, am)
	}

	// Tools.
	for _, td := range req.Tools {
		ar.Tools = append(ar.Tools, anthropicTool{
			Name: td.Name, Description: td.Description, InputSchema: td.Schema,
		})
	}

	return ar
}

// setHeaders configures the HTTP request headers based on auth type.
func (p *AnthropicProvider) setHeaders(req *http.Request, isOAuth bool) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("anthropic-version", "2023-06-01")

	if isOAuth {
		entry := p.auth["anthropic"]
		req.Header.Set("Authorization", "Bearer "+entry.Access)
		req.Header.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20")
		if p.config != nil {
			req.Header.Set("user-agent", "claude-cli/"+p.config.Stealth.CCVersion)
		} else {
			req.Header.Set("user-agent", "claude-cli/2.1.156")
		}
		req.Header.Set("x-app", "cli")
	} else {
		apiKey := auth.GetAPIKey(p.auth, "anthropic")
		if apiKey == "" && p.config != nil {
			apiKey = p.config.Anthropic.APIKey
		}
		req.Header.Set("x-api-key", apiKey)
	}
}

// pumpSSE reads the SSE stream from the response body and converts it to
// StreamEvents on the channel.
func (p *AnthropicProvider) pumpSSE(ctx context.Context, body io.ReadCloser, ch chan<- StreamEvent) {
	defer close(ch)
	defer body.Close()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	var currentToolCall *ToolCall
	toolInputBuffers := make(map[string]*bytes.Buffer)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "event: ") {
			continue
		}

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		var event struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		switch event.Type {
		case "message_start":
			var msg struct {
				Message struct {
					Usage struct {
						InputTokens      int `json:"input_tokens"`
						OutputTokens     int `json:"output_tokens"`
						CacheReadTokens  int `json:"cache_read_input_tokens"`
						CacheWriteTokens int `json:"cache_creation_input_tokens"`
					} `json:"usage"`
				} `json:"message"`
			}
			json.Unmarshal([]byte(data), &msg)
			// We'll send usage with the done event, but store it.
			// For now, emit nothing — usage comes with message_delta.

		case "content_block_start":
			var block struct {
				Index        int `json:"index"`
				ContentBlock struct {
					Type string `json:"type"`
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"content_block"`
			}
			json.Unmarshal([]byte(data), &block)
			if block.ContentBlock.Type == "tool_use" {
				currentToolCall = &ToolCall{
					ID:   block.ContentBlock.ID,
					Name: block.ContentBlock.Name,
				}
				toolInputBuffers[block.ContentBlock.ID] = &bytes.Buffer{}
				select {
				case <-ctx.Done():
					return
				case ch <- StreamEvent{Type: EventToolUseStart, ToolCall: currentToolCall}:
				}
			}

		case "content_block_delta":
			var delta struct {
				Index int `json:"index"`
				Delta struct {
					Type        string `json:"type"`
					Text        string `json:"text"`
					PartialJSON string `json:"partial_json"`
				} `json:"delta"`
			}
			json.Unmarshal([]byte(data), &delta)
			switch delta.Delta.Type {
			case "text_delta":
				select {
				case <-ctx.Done():
					return
				case ch <- StreamEvent{Type: EventTextDelta, Text: delta.Delta.Text}:
				}
			case "input_json_delta":
				if currentToolCall != nil {
					if buf, ok := toolInputBuffers[currentToolCall.ID]; ok {
						buf.WriteString(delta.Delta.PartialJSON)
					}
				}
			}

		case "content_block_stop":
			if currentToolCall != nil {
				if buf, ok := toolInputBuffers[currentToolCall.ID]; ok {
					currentToolCall.Input = json.RawMessage(buf.Bytes())
					delete(toolInputBuffers, currentToolCall.ID)
				}
				select {
				case <-ctx.Done():
					return
				case ch <- StreamEvent{Type: EventToolUseStop, ToolCall: currentToolCall}:
				}
				currentToolCall = nil
			}

		case "message_delta":
			var msgDelta struct {
				Usage struct {
					InputTokens      int `json:"input_tokens"`
					OutputTokens     int `json:"output_tokens"`
					CacheReadTokens  int `json:"cache_read_input_tokens"`
					CacheWriteTokens int `json:"cache_creation_input_tokens"`
				} `json:"usage"`
			}
			json.Unmarshal([]byte(data), &msgDelta)
			usage := &AnthropicUsage{
				Usage: Usage{
					InputTokens:  msgDelta.Usage.InputTokens,
					OutputTokens: msgDelta.Usage.OutputTokens,
				},
				CacheReadTokens:  msgDelta.Usage.CacheReadTokens,
				CacheWriteTokens: msgDelta.Usage.CacheWriteTokens,
			}
			select {
			case <-ctx.Done():
				return
			case ch <- StreamEvent{Type: EventDone, Usage: &usage.Usage}:
			}

		case "message_stop":
			return

		case "error":
			var errEvent struct {
				Error struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				} `json:"error"`
			}
			json.Unmarshal([]byte(data), &errEvent)
			select {
			case <-ctx.Done():
				return
			case ch <- StreamEvent{Type: EventError, Error: fmt.Errorf("anthropic: %s: %s", errEvent.Error.Type, errEvent.Error.Message)}:
			}
		}
	}

	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		select {
		case ch <- StreamEvent{Type: EventError, Error: fmt.Errorf("sse read: %w", err)}:
		default:
		}
	}
}
