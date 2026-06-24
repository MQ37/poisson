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

// Models returns the known xAI Grok models with accurate context windows.
func (p *XAIProvider) Models() ([]Model, error) {
	return []Model{
		{ID: "grok-build", Name: "Grok Build", ContextWindow: 256000},
	}, nil
}

// Stream sends a request to the xAI API (OpenAI-compatible) and returns
// a channel of StreamEvents. Uses OAuth Bearer token authentication with
// auto-refresh on 401.
func (p *XAIProvider) Stream(ctx context.Context, req *Request) (<-chan StreamEvent, error) {
	return p.streamWithRetry(ctx, req, 0)
}

func (p *XAIProvider) streamWithRetry(ctx context.Context, req *Request, retry int) (<-chan StreamEvent, error) {
	// Check for token refresh.
	entry, ok := p.auth["xai"]
	if !ok || entry.Type != "oauth" {
		return nil, fmt.Errorf("no xAI OAuth credentials — run: px login xai")
	}
	if auth.IsExpired(entry, 5*60*1000) {
		refreshed, err := auth.RefreshXAIToken(entry.Refresh)
		if err == nil {
			p.auth["xai"] = *refreshed
			_ = auth.Save(p.auth)
			entry = *refreshed
		}
	}

	// Build OpenAI-compatible request body.
	xaiReq := p.buildRequest(req)
	reqBody, err := json.Marshal(xaiReq)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		"https://api.x.ai/v1/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Authorization", "Bearer "+entry.Access)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, err
	}

	// Handle 401 with refresh + retry.
	if resp.StatusCode == 401 && retry == 0 {
		resp.Body.Close()
		refreshed, err := auth.RefreshXAIToken(entry.Refresh)
		if err == nil {
			p.auth["xai"] = *refreshed
			_ = auth.Save(p.auth)
			return p.streamWithRetry(ctx, req, 1)
		}
		return nil, fmt.Errorf("token expired, refresh failed: %w", err)
	}

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("xAI API error (status %d): %s", resp.StatusCode, string(body))
	}

	ch := make(chan StreamEvent, 64)
	go p.pumpSSE(ctx, resp.Body, ch)
	return ch, nil
}

// xaiRequest is the OpenAI-compatible request body.
type xaiRequest struct {
	Model       string       `json:"model"`
	Messages    []xaiMessage `json:"messages"`
	Tools       []xaiTool    `json:"tools,omitempty"`
	MaxTokens   int          `json:"max_tokens,omitempty"`
	Stream      bool         `json:"stream"`
	Temperature *float64     `json:"temperature,omitempty"`
}

type xaiMessage struct {
	Role       string        `json:"role"`
	Content    *string       `json:"content"`
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

// strPtr returns a pointer to the given string (nil-safe helper).
func strPtr(s string) *string { return &s }
func emptyStrPtr() *string    { v := ""; return &v }

// buildRequest converts a Poisson Request to the xAI OpenAI-compatible format.
// Every message MUST have a content field (even if empty) or xAI returns 422.
func (p *XAIProvider) buildRequest(req *Request) xaiRequest {
	ar := xaiRequest{
		Model:       req.Model,
		Stream:      true,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
	}
	if ar.MaxTokens == 0 {
		ar.MaxTokens = 4096
	}

	// System blocks → system messages.
	for _, sb := range req.System {
		ar.Messages = append(ar.Messages, xaiMessage{
			Role:    "system",
			Content: strPtr(sb.Text),
		})
	}

	// Messages.
	for _, msg := range req.Messages {
		var textParts []string
		var toolCalls []xaiToolCall

		for _, cb := range msg.Content {
			switch cb.Type {
			case "text":
				textParts = append(textParts, cb.Text)
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
				// Tool result is its own message with role "tool".
				ar.Messages = append(ar.Messages, xaiMessage{
					Role:       "tool",
					Content:    strPtr(cb.ToolResult),
					ToolCallID: cb.ToolCallID,
				})
				continue
			}
		}

		// Build the message — content must always be set (not nil).
		xm := xaiMessage{Role: msg.Role}
		if len(textParts) > 0 {
			xm.Content = strPtr(strings.Join(textParts, "\n"))
		} else if msg.Role == "assistant" {
			xm.Content = emptyStrPtr() // assistant with only tool calls still needs content: ""
		} else {
			xm.Content = emptyStrPtr()
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

// pumpSSE reads the OpenAI-compatible SSE stream and converts to StreamEvents.
func (p *XAIProvider) pumpSSE(ctx context.Context, body io.ReadCloser, ch chan<- StreamEvent) {
	defer close(ch)
	defer body.Close()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	var currentToolID string
	var currentToolName string
	var toolInputBuf bytes.Buffer

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, ": ") {
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			return
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		// Usage-only chunk (some providers send usage in a final chunk with no choices).
		if len(chunk.Choices) == 0 {
			if chunk.Usage != nil {
				select {
				case <-ctx.Done():
					return
				case ch <- StreamEvent{Type: EventDone, Usage: &Usage{
					InputTokens:  chunk.Usage.PromptTokens,
					OutputTokens: chunk.Usage.CompletionTokens,
				}}:
				}
			}
			continue
		}

		delta := chunk.Choices[0].Delta

		// Text delta.
		if delta.Content != "" {
			select {
			case <-ctx.Done():
				return
			case ch <- StreamEvent{Type: EventTextDelta, Text: delta.Content}:
			}
		}

		// Tool calls.
		for _, tc := range delta.ToolCalls {
			if tc.ID != "" {
				if currentToolID != "" {
					select {
					case <-ctx.Done():
						return
					case ch <- StreamEvent{Type: EventToolUseStop, ToolCall: &ToolCall{
						ID:    currentToolID,
						Name:  currentToolName,
						Input: json.RawMessage(toolInputBuf.Bytes()),
					}}:
					}
					toolInputBuf.Reset()
				}
				currentToolID = tc.ID
				currentToolName = tc.Function.Name
				select {
				case <-ctx.Done():
					return
				case ch <- StreamEvent{Type: EventToolUseStart, ToolCall: &ToolCall{
					ID:   tc.ID,
					Name: tc.Function.Name,
				}}:
				}
			}
			if tc.Function.Arguments != "" {
				toolInputBuf.WriteString(tc.Function.Arguments)
			}
		}

		// Finish reason.
		if chunk.Choices[0].FinishReason != nil {
			if currentToolID != "" {
				select {
				case <-ctx.Done():
					return
				case ch <- StreamEvent{Type: EventToolUseStop, ToolCall: &ToolCall{
					ID:    currentToolID,
					Name:  currentToolName,
					Input: json.RawMessage(toolInputBuf.Bytes()),
				}}:
				}
				currentToolID = ""
				toolInputBuf.Reset()
			}
			if chunk.Usage != nil {
				select {
				case <-ctx.Done():
					return
				case ch <- StreamEvent{Type: EventDone, Usage: &Usage{
					InputTokens:  chunk.Usage.PromptTokens,
					OutputTokens: chunk.Usage.CompletionTokens,
				}}:
				}
			} else {
				select {
				case <-ctx.Done():
					return
				case ch <- StreamEvent{Type: EventDone, Usage: &Usage{}}:
				}
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
