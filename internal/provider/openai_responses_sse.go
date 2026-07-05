package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// pumpOpenAIResponsesSSE reads the Codex Responses SSE stream and translates
// its `response.*` events into Poisson StreamEvents. It owns the body and the
// channel (both closed on return). EventDone or EventError is the last event on
// the normal path; on ctx cancellation it returns without emitting either.
//
// Function-call arguments stream as deltas keyed by output_index; text and
// reasoning-summary deltas map to text/thinking events. See the pi.dev Codex
// mapping (packages/ai/src/providers/openai-responses-shared.ts).
func pumpOpenAIResponsesSSE(ctx context.Context, body io.ReadCloser, ch chan<- StreamEvent) {
	defer body.Close()
	defer close(ch)

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	// output_index → in-flight tool call and its accumulated argument JSON.
	calls := make(map[int]*ToolCall)
	argBuf := make(map[int]*strings.Builder)
	stopped := make(map[int]bool)

	send := func(ev StreamEvent) bool {
		select {
		case <-ctx.Done():
			return false
		case ch <- ev:
			return true
		}
	}
	finalize := func(idx int, args string) bool {
		if stopped[idx] {
			return true
		}
		call := calls[idx]
		if call == nil {
			return true
		}
		stopped[idx] = true
		if args == "" {
			if b := argBuf[idx]; b != nil {
				args = b.String()
			}
		}
		return send(StreamEvent{Type: EventToolUseStop, ToolCall: &ToolCall{ID: call.ID, Name: call.Name, Input: json.RawMessage(args)}})
	}

	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}

		var ev struct {
			Type        string `json:"type"`
			Delta       string `json:"delta"`
			Arguments   string `json:"arguments"`
			OutputIndex int    `json:"output_index"`
			Item        struct {
				Type      string `json:"type"`
				CallID    string `json:"call_id"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"item"`
			Response struct {
				Usage *openaiRespUsage `json:"usage"`
				Error *struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			} `json:"response"`
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}

		switch ev.Type {
		case "response.output_text.delta":
			if ev.Delta != "" && !send(StreamEvent{Type: EventTextDelta, Text: ev.Delta}) {
				return
			}
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			if ev.Delta != "" && !send(StreamEvent{Type: EventThinkingDelta, Text: ev.Delta}) {
				return
			}
		case "response.reasoning_summary_part.done":
			if !send(StreamEvent{Type: EventThinkingDelta, Text: "\n\n"}) {
				return
			}
		case "response.output_item.added":
			if ev.Item.Type == "function_call" {
				call := &ToolCall{ID: ev.Item.CallID, Name: ev.Item.Name}
				calls[ev.OutputIndex] = call
				b := &strings.Builder{}
				b.WriteString(ev.Item.Arguments)
				argBuf[ev.OutputIndex] = b
				if !send(StreamEvent{Type: EventToolUseStart, ToolCall: &ToolCall{ID: call.ID, Name: call.Name}}) {
					return
				}
			}
		case "response.function_call_arguments.delta":
			b := argBuf[ev.OutputIndex]
			call := calls[ev.OutputIndex]
			if b == nil || call == nil {
				continue
			}
			b.WriteString(ev.Delta)
			if !send(StreamEvent{Type: EventToolUseDelta, ToolCall: &ToolCall{ID: call.ID, Name: call.Name, Input: json.RawMessage(b.String())}}) {
				return
			}
		case "response.function_call_arguments.done":
			if !finalize(ev.OutputIndex, ev.Arguments) {
				return
			}
		case "response.output_item.done":
			if ev.Item.Type == "function_call" && !finalize(ev.OutputIndex, ev.Item.Arguments) {
				return
			}
		case "response.completed":
			send(StreamEvent{Type: EventDone, Usage: convertOpenAIRespUsage(ev.Response.Usage)})
			return
		case "response.failed":
			msg := "OpenAI response failed"
			if e := ev.Response.Error; e != nil {
				msg = fmt.Sprintf("OpenAI error %s: %s", e.Code, e.Message)
			}
			send(StreamEvent{Type: EventError, Error: fmt.Errorf("%s", msg)})
			return
		case "error":
			send(StreamEvent{Type: EventError, Error: fmt.Errorf("OpenAI error %s: %s", ev.Code, ev.Message)})
			return
		}
	}

	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		send(StreamEvent{Type: EventError, Error: fmt.Errorf("openai: read stream: %w", err)})
	}
}

// openaiRespUsage is the usage object on the response.completed event.
type openaiRespUsage struct {
	InputTokens        int `json:"input_tokens"`
	InputTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// convertOpenAIRespUsage maps Responses usage to Poisson Usage. OpenAI counts
// cached tokens inside input_tokens, so we split them out.
func convertOpenAIRespUsage(u *openaiRespUsage) *Usage {
	if u == nil {
		return nil
	}
	return &Usage{
		InputTokens:     u.InputTokens - u.InputTokensDetails.CachedTokens,
		OutputTokens:    u.OutputTokens,
		CacheReadTokens: u.InputTokensDetails.CachedTokens,
	}
}
