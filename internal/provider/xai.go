package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
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
	Model         string            `json:"model"`
	Messages      []xaiMessage      `json:"messages"`
	Tools         []xaiTool         `json:"tools,omitempty"`
	MaxTokens     int               `json:"max_tokens,omitempty"`
	Stream        bool              `json:"stream"`
	StreamOptions *xaiStreamOptions `json:"stream_options,omitempty"`
	Temperature   *float64          `json:"temperature,omitempty"`
}

type xaiStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
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
		Model:         req.Model,
		Stream:        true,
		StreamOptions: &xaiStreamOptions{IncludeUsage: true},
		MaxTokens:     req.MaxTokens,
		Temperature:   req.Temperature,
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
		if msg.Role == "tool" {
			for _, cb := range msg.Content {
				if cb.Type != "tool_result" {
					continue
				}
				ar.Messages = append(ar.Messages, xaiMessage{
					Role:       "tool",
					Content:    strPtr(cb.ToolResult),
					ToolCallID: cb.ToolCallID,
				})
			}
			continue
		}

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

type xaiAPIUsage struct {
	PromptTokens            int `json:"prompt_tokens"`
	CompletionTokens        int `json:"completion_tokens"`
	TotalTokens             int `json:"total_tokens"`
	CompletionTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

func convertXAIUsage(u *xaiAPIUsage) *Usage {
	if u == nil {
		return nil
	}
	output := u.CompletionTokens + u.CompletionTokensDetails.ReasoningTokens
	if totalOutput := u.TotalTokens - u.PromptTokens; totalOutput > output {
		output = totalOutput
	}
	return &Usage{InputTokens: u.PromptTokens, OutputTokens: output}
}

// pumpSSE reads the OpenAI-compatible SSE stream and converts to StreamEvents.
func (p *XAIProvider) pumpSSE(ctx context.Context, body io.ReadCloser, ch chan<- StreamEvent) {
	defer close(ch)
	defer body.Close()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	toolCalls := make(map[int]*ToolCall)
	toolInputBuffers := make(map[int]*bytes.Buffer)
	finishSeen := false
	doneSent := false
	sendDone := func(usage *Usage) bool {
		select {
		case <-ctx.Done():
			return false
		case ch <- StreamEvent{Type: EventDone, Usage: usage}:
			doneSent = true
			return true
		}
	}

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
			if finishSeen && !doneSent {
				sendDone(&Usage{})
			}
			return
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
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
			Usage *xaiAPIUsage `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		// xAI sends final usage as a separate chunk when stream_options.include_usage=true.
		if len(chunk.Choices) == 0 {
			if usage := convertXAIUsage(chunk.Usage); usage != nil {
				sendDone(usage)
				return
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
			idx := tc.Index
			if tc.ID != "" {
				call := &ToolCall{ID: tc.ID, Name: tc.Function.Name}
				toolCalls[idx] = call
				toolInputBuffers[idx] = &bytes.Buffer{}
				select {
				case <-ctx.Done():
					return
				case ch <- StreamEvent{Type: EventToolUseStart, ToolCall: call}:
				}
			}
			if tc.Function.Arguments != "" {
				buf := toolInputBuffers[idx]
				if buf == nil {
					buf = &bytes.Buffer{}
					toolInputBuffers[idx] = buf
				}
				buf.WriteString(tc.Function.Arguments)
			}
		}

		// Finish reason.
		if chunk.Choices[0].FinishReason != nil {
			finishSeen = true
			if len(toolCalls) > 0 {
				idxs := make([]int, 0, len(toolCalls))
				for idx := range toolCalls {
					idxs = append(idxs, idx)
				}
				sort.Ints(idxs)
				for _, idx := range idxs {
					call := toolCalls[idx]
					if buf := toolInputBuffers[idx]; buf != nil {
						call.Input = json.RawMessage(buf.Bytes())
					}
					select {
					case <-ctx.Done():
						return
					case ch <- StreamEvent{Type: EventToolUseStop, ToolCall: call}:
					}
				}
				toolCalls = make(map[int]*ToolCall)
				toolInputBuffers = make(map[int]*bytes.Buffer)
			}
			if usage := convertXAIUsage(chunk.Usage); usage != nil {
				sendDone(usage)
				return
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
