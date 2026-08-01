package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// openaiSSEUsage is the usage object on OpenAI-compatible streaming chunks.
type openaiSSEUsage struct {
	PromptTokens            int `json:"prompt_tokens"`
	CompletionTokens        int `json:"completion_tokens"`
	TotalTokens             int `json:"total_tokens"`
	CompletionTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
	// PromptTokensDetails.CachedTokens is xAI's prompt-cache-hit count
	// (https://docs.x.ai — same shape as OpenAI's chat-completions API).
	// Ollama shares this struct but never populates the field; harmless.
	PromptTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

// openaiSSEConfig tunes provider-specific behavior for the shared SSE pump.
type openaiSSEConfig struct {
	InputEstimate          int
	ConvertUsage           func(*openaiSSEUsage, int) *Usage
	EmitToolDeltas         bool
	AllowNameOnlyToolStart bool
	FailOnParseError       bool
	EnsureDoneOnEOF        bool
	ErrPrefix              string
}

func pumpOpenAIChatCompletionsSSE(ctx context.Context, body io.ReadCloser, ch chan<- StreamEvent, cfg openaiSSEConfig) {
	defer body.Close()
	defer close(ch)

	prefix := cfg.ErrPrefix
	if prefix == "" {
		prefix = "openai-sse"
	}

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	toolCalls := make(map[int]*ToolCall)
	toolInputBuffers := make(map[int]*bytes.Buffer)
	finishSeen := false
	doneSent := false
	sendDone := func(usage *Usage) bool {
		if usage == nil && cfg.EnsureDoneOnEOF {
			usage = &Usage{
				InputTokens:        cfg.InputEstimate,
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
				sendDone(nil)
			}
			return
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"` // ollama / DeepSeek-style
					Reasoning        string `json:"reasoning"`         // xAI / alternate
					ToolCalls        []struct {
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
			Usage *openaiSSEUsage `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			if cfg.FailOnParseError {
				select {
				case ch <- StreamEvent{Type: EventError, Error: fmt.Errorf("%s: parse chunk: %w", prefix, err)}:
				case <-ctx.Done():
				}
				return
			}
			continue
		}

		if len(chunk.Choices) == 0 {
			if usage := cfg.ConvertUsage(chunk.Usage, cfg.InputEstimate); usage != nil {
				sendDone(usage)
				return
			}
			continue
		}

		delta := chunk.Choices[0].Delta

		// Reasoning / thinking content — emitted before the answer so the TUI
		// groups it as a thinking block (ollama reasoning_content, xAI reasoning).
		if delta.ReasoningContent != "" {
			select {
			case <-ctx.Done():
				return
			case ch <- StreamEvent{Type: EventThinkingDelta, Text: delta.ReasoningContent}:
			}
		}
		if delta.Reasoning != "" {
			select {
			case <-ctx.Done():
				return
			case ch <- StreamEvent{Type: EventThinkingDelta, Text: delta.Reasoning}:
			}
		}

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
			} else if cfg.AllowNameOnlyToolStart && tc.Function.Name != "" && toolCalls[idx] == nil {
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
				if cfg.EmitToolDeltas {
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
			if usage := cfg.ConvertUsage(chunk.Usage, cfg.InputEstimate); usage != nil {
				sendDone(usage)
				return
			}
		}
	}

	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		select {
		case ch <- StreamEvent{Type: EventError, Error: fmt.Errorf("%s: read stream: %w", prefix, err)}:
		case <-ctx.Done():
		}
		return
	}

	if cfg.EnsureDoneOnEOF && !doneSent {
		sendDone(nil)
	}
}
