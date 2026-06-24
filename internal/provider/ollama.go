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
	"time"
)

// defaultOllamaContextWindow is used when the Ollama /api/tags response does
// not report a model's context length (older Ollama builds). It is only a
// fallback for display/triggers; the provider never rejects requests based on
// it.
const defaultOllamaContextWindow = 8192

// OllamaProvider talks to a local (or remote) Ollama instance using its
// native /api/chat and /api/tags endpoints. It requires no authentication.
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

// --- Wire types -------------------------------------------------------

type ollamaChatRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Tools    []ollamaTool    `json:"tools,omitempty"`
	Stream   bool            `json:"stream"`
	Options  ollamaOptions   `json:"options,omitempty"`
}

type ollamaMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
}

type ollamaToolCall struct {
	ID       string         `json:"id,omitempty"`
	Function ollamaToolFunc `json:"function"`
}

type ollamaToolFunc struct {
	Index     int             `json:"index,omitempty"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type ollamaTool struct {
	Type     string        `json:"type"` // always "function"
	Function ollamaToolDef `json:"function"`
}

type ollamaToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type ollamaOptions struct {
	Temperature float64 `json:"temperature,omitempty"`
	NumPredict  int     `json:"num_predict,omitempty"`
	Think       bool    `json:"think,omitempty"`
}

// ollamaChatResponse is one NDJSON line from the streaming /api/chat body.
type ollamaChatResponse struct {
	Model           string            `json:"model"`
	Message         ollamaRespMessage `json:"message"`
	Done            bool              `json:"done"`
	DoneReason      string            `json:"done_reason,omitempty"`
	PromptEvalCount int               `json:"prompt_eval_count,omitempty"`
	EvalCount       int               `json:"eval_count,omitempty"`
}

type ollamaRespMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
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

// --- Request mapping --------------------------------------------------

// buildOllamaRequest converts a provider.Request into the Ollama /api/chat
// payload. System blocks become a leading "system" message; message content
// blocks are flattened into a content string (and tool_use / tool_result
// blocks into tool_calls / role "tool" messages respectively).
func (p *OllamaProvider) buildOllamaRequest(req *Request) *ollamaChatRequest {
	out := &ollamaChatRequest{
		Model:  req.Model,
		Stream: true,
	}
	if out.Model == "" {
		out.Model = p.model
	}

	// System blocks → leading system message.
	if len(req.System) > 0 {
		var sb strings.Builder
		for _, b := range req.System {
			if sb.Len() > 0 {
				sb.WriteString("\n\n")
			}
			sb.WriteString(b.Text)
		}
		out.Messages = append(out.Messages, ollamaMessage{
			Role:    "system",
			Content: sb.String(),
		})
	}

	// Conversation messages.
	for _, m := range req.Messages {
		om := ollamaMessage{Role: m.Role}
		var content strings.Builder
		for _, b := range m.Content {
			switch b.Type {
			case "text":
				content.WriteString(b.Text)
			case "tool_use":
				om.ToolCalls = append(om.ToolCalls, ollamaToolCall{
					ID: b.ToolCallID,
					Function: ollamaToolFunc{
						Name:      b.ToolName,
						Arguments: b.ToolInput,
					},
				})
			case "tool_result":
				// A tool result is conveyed as the message content. If the
				// block carries a call id, prefix it for traceability.
				if b.ToolCallID != "" {
					content.WriteString(fmt.Sprintf("[tool:%s] ", b.ToolCallID))
				}
				content.WriteString(b.ToolResult)
			}
		}
		om.Content = content.String()
		out.Messages = append(out.Messages, om)
	}

	// Tools.
	for _, t := range req.Tools {
		out.Tools = append(out.Tools, ollamaTool{
			Type: "function",
			Function: ollamaToolDef{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Schema,
			},
		})
	}

	// Options.
	{
		opts := ollamaOptions{NumPredict: req.MaxTokens}
		if req.Temperature != nil {
			opts.Temperature = *req.Temperature
		}
		// Map effort to think=true for models that support it.
		if req.Effort != "" {
			opts.Think = true
		}
		if opts.Temperature != 0 || opts.NumPredict != 0 || opts.Think {
			out.Options = opts
		}
	}

	return out
}

// --- Stream -----------------------------------------------------------

// Stream POSTs to {baseURL}/api/chat with stream:true and parses the NDJSON
// response line by line, emitting StreamEvents on the returned channel.
//
// See the Provider interface for the channel lifecycle contract.
func (p *OllamaProvider) Stream(ctx context.Context, req *Request) (<-chan StreamEvent, error) {
	body := p.buildOllamaRequest(req)
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("ollama: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/api/chat", bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("ollama: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/x-ndjson")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama: request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		// Read a small amount of the body for diagnostics, then close.
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		return nil, fmt.Errorf("ollama: /api/chat returned %s: %s", resp.Status, strings.TrimSpace(string(snippet)))
	}

	ch := make(chan StreamEvent, 32)
	go p.pump(ctx, resp.Body, ch)
	return ch, nil
}

// pump reads the NDJSON stream, decodes each line, and emits StreamEvents.
// It owns resp.Body and closes it on exit. The channel is always closed
// (defer close(ch)).
func (p *OllamaProvider) pump(ctx context.Context, body io.ReadCloser, ch chan<- StreamEvent) {
	defer body.Close()
	defer close(ch)

	// activeToolCalls tracks in-flight tool calls by key (id, or
	// "idx_<index>" when the provider omits an id). It lets us emit
	// Start/Delta/Stop correctly whether arguments arrive whole (as a JSON
	// object) or incrementally (as a JSON string).
	type activeTC struct {
		call *ToolCall
	}
	active := make(map[string]*activeTC)

	scanner := bufio.NewScanner(body)
	// Some Ollama chunks can be large (e.g. big tool inputs); raise the
	// per-line limit well above the 64KB default.
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	for scanner.Scan() {
		// Honour cancellation promptly: close without a done/error event.
		if err := ctx.Err(); err != nil {
			return
		}
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var chunk ollamaChatResponse
		if err := json.Unmarshal(line, &chunk); err != nil {
			// Mid-stream parse error: emit EventError then close.
			select {
			case ch <- StreamEvent{Type: EventError, Error: fmt.Errorf("ollama: parse chunk: %w", err)}:
			case <-ctx.Done():
			}
			return
		}

		// Text delta.
		if chunk.Message.Content != "" {
			select {
			case ch <- StreamEvent{Type: EventTextDelta, Text: chunk.Message.Content}:
			case <-ctx.Done():
				return
			}
		}

		// Tool calls.
		for _, tc := range chunk.Message.ToolCalls {
			key := tc.ID
			if key == "" {
				key = fmt.Sprintf("idx_%d", tc.Function.Index)
			}
			args := normalizeToolArgs(tc.Function.Arguments)
			if existing, ok := active[key]; ok {
				// Subsequent delta for an in-flight call.
				updated := &ToolCall{
					ID:    existing.call.ID,
					Name:  existing.call.Name,
					Input: args,
				}
				existing.call = updated
				select {
				case ch <- StreamEvent{Type: EventToolUseDelta, ToolCall: updated}:
				case <-ctx.Done():
					return
				}
			} else {
				call := &ToolCall{
					ID:    tc.ID,
					Name:  tc.Function.Name,
					Input: args,
				}
				active[key] = &activeTC{call: call}
				select {
				case ch <- StreamEvent{Type: EventToolUseStart, ToolCall: call}:
				case <-ctx.Done():
					return
				}
			}
		}

		// Final chunk: emit Stop for any active tool calls, then Done.
		if chunk.Done {
			for key, a := range active {
				select {
				case ch <- StreamEvent{Type: EventToolUseStop, ToolCall: a.call}:
				case <-ctx.Done():
					return
				}
				delete(active, key)
			}
			usage := &Usage{
				InputTokens:  chunk.PromptEvalCount,
				OutputTokens: chunk.EvalCount,
			}
			select {
			case ch <- StreamEvent{Type: EventDone, Usage: usage}:
			case <-ctx.Done():
				return
			}
			return
		}
	}

	if err := scanner.Err(); err != nil {
		// If the context was cancelled, treat as cancellation (no error).
		if ctx.Err() != nil {
			return
		}
		select {
		case ch <- StreamEvent{Type: EventError, Error: fmt.Errorf("ollama: read stream: %w", err)}:
		case <-ctx.Done():
		}
		return
	}

	// Stream ended without a done:true chunk. Emit a synthetic Done so the
	// caller's range completes cleanly (usage will be zero if unavailable).
	select {
	case ch <- StreamEvent{Type: EventDone, Usage: &Usage{}}:
	case <-ctx.Done():
	}
}

// normalizeToolArgs coerces a tool-call arguments value into a canonical
// json.RawMessage. Ollama may emit arguments as either a JSON object
// (already raw bytes) or as a JSON string that needs to be parsed/re-marshaled.
// We accept both and return valid JSON object bytes (or nil if empty).
func normalizeToolArgs(raw json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	// If it's a JSON string, unwrap it: Ollama sometimes streams arguments as
	// an accumulating string that itself contains JSON.
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("\"")) {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			trimmed := strings.TrimSpace(s)
			if trimmed == "" {
				return nil
			}
			// If the string itself is valid JSON, return it directly.
			var obj json.RawMessage
			if json.Unmarshal([]byte(trimmed), &obj) == nil {
				return json.RawMessage(trimmed)
			}
			// Otherwise wrap as a JSON string.
			b, _ := json.Marshal(trimmed)
			return b
		}
	}
	return raw
}

// --- Models -----------------------------------------------------------

// Models lists the models installed on the Ollama instance via GET /api/tags.
// The context window is taken from details.context_length when present,
// falling back to defaultOllamaContextWindow.
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
			ID:            t.Name,
			Name:          name,
			ContextWindow: cw,
		})
	}
	return models, nil
}
