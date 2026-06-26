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
	"time"
)

// defaultOllamaContextWindow is used when the Ollama /api/tags response does
// not report a model's context length (older Ollama builds). It is only a
// fallback for display/triggers; the provider never rejects requests based on
// it.
const defaultOllamaContextWindow = 8192

// OllamaProvider talks to a local (or remote) Ollama instance using the
// OpenAI-compatible /v1/chat/completions endpoint (for accurate token usage on
// cloud models) and /api/tags for model listing.
type OllamaProvider struct {
	baseURL string
	model   string
	client  *http.Client
}

// NewOllamaProvider returns a provider backed by the Ollama instance at
// baseURL (e.g. "http://localhost:11434"). model is the default model used
// when a Request does not specify one.
func NewOllamaProvider(baseURL, model string) *OllamaProvider {
	return &OllamaProvider{
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		client:  &http.Client{Timeout: 0}, // streaming: no overall timeout
	}
}

// ID returns "ollama".
func (p *OllamaProvider) ID() string { return "ollama" }

// --- Wire types (OpenAI-compatible) -----------------------------------

type ollamaChatRequest struct {
	Model           string                `json:"model"`
	Messages        []ollamaOpenAIMessage `json:"messages"`
	Tools           []ollamaOpenAITool    `json:"tools,omitempty"`
	MaxTokens       int                   `json:"max_tokens,omitempty"`
	Stream          bool                  `json:"stream"`
	StreamOptions   *ollamaStreamOptions  `json:"stream_options,omitempty"`
	Temperature     *float64              `json:"temperature,omitempty"`
	ReasoningEffort string                `json:"reasoning_effort,omitempty"`
}

type ollamaStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type ollamaOpenAIMessage struct {
	Role       string                 `json:"role"`
	Content    *string                `json:"content"`
	ToolCalls  []ollamaOpenAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string                 `json:"tool_call_id,omitempty"`
}

type ollamaOpenAIToolCall struct {
	ID       string                   `json:"id"`
	Type     string                   `json:"type"`
	Function ollamaOpenAIToolFunction `json:"function"`
}

type ollamaOpenAIToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ollamaOpenAITool struct {
	Type     string              `json:"type"`
	Function ollamaOpenAIToolDef `json:"function"`
}

type ollamaOpenAIToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type ollamaAPIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ollamaTagsResponse is the body of GET /api/tags.
type ollamaTagsResponse struct {
	Models []ollamaTag `json:"models"`
}

type ollamaTag struct {
	Name    string           `json:"name"`
	Model   string           `json:"model"`
	Details ollamaTagDetails `json:"details"`
}

type ollamaTagDetails struct {
	ContextLength int `json:"context_length"`
}

func ollamaStrPtr(s string) *string { return &s }
func ollamaEmptyStrPtr() *string    { v := ""; return &v }

func mapOllamaReasoningEffort(effort string) string {
	switch effort {
	case "low", "medium", "high", "max":
		return effort
	case "xhigh":
		return "high"
	default:
		return ""
	}
}

func estimateOllamaRequestTokens(req *Request) int {
	if req == nil {
		return 0
	}
	var b strings.Builder
	for _, sb := range req.System {
		b.WriteString(sb.Text)
	}
	for _, m := range req.Messages {
		for _, cb := range m.Content {
			switch cb.Type {
			case "text":
				b.WriteString(cb.Text)
			case "tool_result":
				b.WriteString(cb.ToolResult)
			case "tool_use":
				b.WriteString(cb.ToolName)
				b.Write(cb.ToolInput)
			}
		}
	}
	for _, t := range req.Tools {
		b.WriteString(t.Name)
		b.WriteString(t.Description)
		b.Write(t.Schema)
	}
	n := len(b.String())
	if n == 0 {
		return 0
	}
	if n < 4 {
		return 1
	}
	return n / 4
}

func convertOllamaUsage(u *ollamaAPIUsage, inputEstimate int) *Usage {
	if u == nil {
		return nil
	}
	input := u.PromptTokens
	inputUnknown := false
	if input == 0 && u.CompletionTokens > 0 {
		if inputEstimate > 0 {
			input = inputEstimate
			inputUnknown = true
		} else {
			inputUnknown = true
		}
	}
	return &Usage{
		InputTokens:        input,
		OutputTokens:       u.CompletionTokens,
		InputTokensUnknown: inputUnknown,
	}
}

// buildOllamaRequest converts a provider.Request into the OpenAI-compatible
// /v1/chat/completions payload.
func (p *OllamaProvider) buildOllamaRequest(req *Request) ollamaChatRequest {
	out := ollamaChatRequest{
		Model:  req.Model,
		Stream: true,
		StreamOptions: &ollamaStreamOptions{
			IncludeUsage: true,
		},
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
	}
	if out.Model == "" {
		out.Model = p.model
	}
	if re := mapOllamaReasoningEffort(req.Effort); re != "" {
		out.ReasoningEffort = re
	}

	for _, b := range req.System {
		out.Messages = append(out.Messages, ollamaOpenAIMessage{
			Role:    "system",
			Content: ollamaStrPtr(b.Text),
		})
	}

	for _, m := range req.Messages {
		if m.Role == "tool" {
			for _, cb := range m.Content {
				if cb.Type != "tool_result" {
					continue
				}
				out.Messages = append(out.Messages, ollamaOpenAIMessage{
					Role:       "tool",
					Content:    ollamaStrPtr(cb.ToolResult),
					ToolCallID: cb.ToolCallID,
				})
			}
			continue
		}

		var textParts []string
		var toolCalls []ollamaOpenAIToolCall
		for _, cb := range m.Content {
			switch cb.Type {
			case "text":
				textParts = append(textParts, cb.Text)
			case "tool_use":
				toolCalls = append(toolCalls, ollamaOpenAIToolCall{
					ID:   cb.ToolCallID,
					Type: "function",
					Function: ollamaOpenAIToolFunction{
						Name:      cb.ToolName,
						Arguments: string(cb.ToolInput),
					},
				})
			}
		}

		om := ollamaOpenAIMessage{Role: m.Role}
		if len(textParts) > 0 {
			om.Content = ollamaStrPtr(strings.Join(textParts, "\n"))
		} else if m.Role == "assistant" || m.Role == "user" {
			om.Content = ollamaEmptyStrPtr()
		}
		if len(toolCalls) > 0 {
			om.ToolCalls = toolCalls
		}
		out.Messages = append(out.Messages, om)
	}

	for _, t := range req.Tools {
		out.Tools = append(out.Tools, ollamaOpenAITool{
			Type: "function",
			Function: ollamaOpenAIToolDef{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Schema,
			},
		})
	}

	return out
}

// Stream POSTs to {baseURL}/v1/chat/completions with stream_options.include_usage
// and parses the SSE response, emitting StreamEvents on the returned channel.
func (p *OllamaProvider) Stream(ctx context.Context, req *Request) (<-chan StreamEvent, error) {
	body := p.buildOllamaRequest(req)
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("ollama: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("ollama: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama: request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		return nil, fmt.Errorf("ollama: /v1/chat/completions returned %s: %s", resp.Status, strings.TrimSpace(string(snippet)))
	}

	inputEstimate := estimateOllamaRequestTokens(req)

	ch := make(chan StreamEvent, 32)
	go p.pumpSSE(ctx, resp.Body, ch, inputEstimate)
	return ch, nil
}

// pumpSSE reads the OpenAI-compatible SSE stream and converts to StreamEvents.
func (p *OllamaProvider) pumpSSE(ctx context.Context, body io.ReadCloser, ch chan<- StreamEvent, inputEstimate int) {
	defer body.Close()
	defer close(ch)

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	toolCalls := make(map[int]*ToolCall)
	toolInputBuffers := make(map[int]*bytes.Buffer)
	finishSeen := false
	doneSent := false
	sendDone := func(usage *Usage) bool {
		if usage == nil {
			usage = &Usage{
				InputTokens:        inputEstimate,
				InputTokensUnknown: true,
			}
		}
		select {
		case <-ctx.Done():
			return false
		case ch <- StreamEvent{Type: EventDone, Usage: usage}:
			doneSent = true
			return true
		}
	}

	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}

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
				sendDone(&Usage{
					InputTokens:        inputEstimate,
					InputTokensUnknown: true,
				})
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
			Usage *ollamaAPIUsage `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			select {
			case ch <- StreamEvent{Type: EventError, Error: fmt.Errorf("ollama: parse chunk: %w", err)}:
			case <-ctx.Done():
			}
			return
		}

		// Usage-only chunk (common for cloud models with include_usage).
		if len(chunk.Choices) == 0 {
			if usage := convertOllamaUsage(chunk.Usage, inputEstimate); usage != nil {
				sendDone(usage)
				return
			}
			continue
		}

		delta := chunk.Choices[0].Delta

		if delta.Content != "" {
			select {
			case <-ctx.Done():
				return
			case ch <- StreamEvent{Type: EventTextDelta, Text: delta.Content}:
			}
		}

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
			} else if tc.Function.Name != "" && toolCalls[idx] == nil {
				key := fmt.Sprintf("idx_%d", idx)
				call := &ToolCall{ID: key, Name: tc.Function.Name}
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
				if call := toolCalls[idx]; call != nil {
					updated := &ToolCall{
						ID:    call.ID,
						Name:  call.Name,
						Input: json.RawMessage(buf.Bytes()),
					}
					toolCalls[idx] = updated
					select {
					case <-ctx.Done():
						return
					case ch <- StreamEvent{Type: EventToolUseDelta, ToolCall: updated}:
					}
				}
			}
		}

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
					if buf := toolInputBuffers[idx]; buf != nil && len(buf.Bytes()) > 0 {
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
			if usage := convertOllamaUsage(chunk.Usage, inputEstimate); usage != nil {
				sendDone(usage)
				return
			}
		}
	}

	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		select {
		case ch <- StreamEvent{Type: EventError, Error: fmt.Errorf("ollama: read stream: %w", err)}:
		case <-ctx.Done():
		}
		return
	}

	if !doneSent {
		sendDone(&Usage{
			InputTokens:        inputEstimate,
			InputTokensUnknown: true,
		})
	}
}

// Models lists the models installed on the Ollama instance via GET /api/tags.
func (p *OllamaProvider) Models() ([]Model, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/api/tags", nil)
	if err != nil {
		return nil, fmt.Errorf("ollama: build tags request: %w", err)
	}
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama: tags request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("ollama: /api/tags returned %s: %s", resp.Status, strings.TrimSpace(string(snippet)))
	}

	var tags ollamaTagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return nil, fmt.Errorf("ollama: decode tags: %w", err)
	}

	models := make([]Model, 0, len(tags.Models))
	for _, t := range tags.Models {
		cw := t.Details.ContextLength
		if cw <= 0 {
			cw = defaultOllamaContextWindow
		}
		name := t.Name
		if name == "" {
			name = t.Model
		}
		models = append(models, Model{
			ID:            name,
			Name:          name,
			ContextWindow: cw,
		})
	}
	return models, nil
}
