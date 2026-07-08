package provider

import (
	"context"
	"encoding/json"
)

// FakeProvider is a test-double that implements the Provider interface
// with pre-programmed responses. It never makes real HTTP calls.
type FakeProvider struct {
	id     string
	models []Model
	// responses is a sequence of responses to return for successive
	// Stream calls. Each response is a list of events to emit on the
	// channel. If responses is exhausted, an empty done event is sent.
	responses [][]StreamEvent
	callCount int
	// lastRequest captures the most recent Request passed to Stream.
	lastRequest *Request
	// requests captures every Request passed to Stream, in call order.
	requests []*Request
}

// NewFakeProvider creates a FakeProvider with the given id and model list.
func NewFakeProvider(id string, models []Model) *FakeProvider {
	return &FakeProvider{id: id, models: models}
}

// SetResponses configures the event sequences to return for successive
// Stream calls. Each element is the full event list for one Stream call.
func (p *FakeProvider) SetResponses(responses [][]StreamEvent) {
	p.responses = responses
}

// ID returns the provider id.
func (p *FakeProvider) ID() string { return p.id }

// Models returns the configured model list.
func (p *FakeProvider) Models() ([]Model, error) { return p.models, nil }

// LastRequest returns the most recent Request passed to Stream (for assertions).
func (p *FakeProvider) LastRequest() *Request { return p.lastRequest }

// Requests returns every Request passed to Stream, in call order (for
// assertions that need to inspect a specific turn, not just the last one).
func (p *FakeProvider) Requests() []*Request { return p.requests }

// CallCount returns the number of times Stream was called.
func (p *FakeProvider) CallCount() int { return p.callCount }

// Stream returns a channel that emits the pre-programmed events for the
// next response in the sequence. If responses are exhausted, it emits a
// single EventDone with zero usage.
func (p *FakeProvider) Stream(ctx context.Context, req *Request) (<-chan StreamEvent, error) {
	p.callCount++
	p.lastRequest = req
	p.requests = append(p.requests, req)

	var events []StreamEvent
	if p.callCount <= len(p.responses) {
		events = p.responses[p.callCount-1]
	} else {
		events = []StreamEvent{{Type: EventDone, Usage: &Usage{InputTokens: 10, OutputTokens: 5}}}
	}

	ch := make(chan StreamEvent, len(events)+1)
	go func() {
		defer close(ch)
		for _, ev := range events {
			select {
			case <-ctx.Done():
				return
			case ch <- ev:
			}
		}
	}()
	return ch, nil
}

// --- Helpers for building fake event sequences ------------------------

// FakeTextResponse builds an event sequence for a simple text response
// (no tool calls). Usage is attached to the done event.
func FakeTextResponse(text string, usage *Usage) []StreamEvent {
	if usage == nil {
		usage = &Usage{InputTokens: 10, OutputTokens: 5}
	}
	return []StreamEvent{
		{Type: EventTextDelta, Text: text},
		{Type: EventDone, Usage: usage},
	}
}

// FakeToolCallResponse builds an event sequence where the assistant
// requests a tool call, then (on the next Stream call) responds with
// text after receiving the tool result. Returns two event sequences:
// the first for the tool-call turn, the second for the final text turn.
func FakeToolCallResponse(toolName string, toolInput interface{}, finalText string) ([]StreamEvent, []StreamEvent) {
	inputJSON, _ := json.Marshal(toolInput)
	first := []StreamEvent{
		{Type: EventTextDelta, Text: "Let me check that."},
		{Type: EventToolUseStart, ToolCall: &ToolCall{ID: "call_1", Name: toolName, Input: inputJSON}},
		{Type: EventToolUseStop, ToolCall: &ToolCall{ID: "call_1", Name: toolName, Input: inputJSON}},
		{Type: EventDone, Usage: &Usage{InputTokens: 20, OutputTokens: 10}},
	}
	second := []StreamEvent{
		{Type: EventTextDelta, Text: finalText},
		{Type: EventDone, Usage: &Usage{InputTokens: 30, OutputTokens: 15}},
	}
	return first, second
}

// FakeMultiToolCallResponse builds an event sequence where the assistant
// requests two tool calls in one turn, then (on the next Stream call) responds
// with text. Returns the first and second event sequences.
func FakeMultiToolCallResponse(toolName1 string, toolInput1 interface{}, toolName2 string, toolInput2 interface{}, finalText string) ([]StreamEvent, []StreamEvent) {
	inputJSON1, _ := json.Marshal(toolInput1)
	inputJSON2, _ := json.Marshal(toolInput2)
	first := []StreamEvent{
		{Type: EventTextDelta, Text: "I will run both tools."},
		{Type: EventToolUseStart, ToolCall: &ToolCall{ID: "call_1", Name: toolName1, Input: inputJSON1}},
		{Type: EventToolUseStop, ToolCall: &ToolCall{ID: "call_1", Name: toolName1, Input: inputJSON1}},
		{Type: EventToolUseStart, ToolCall: &ToolCall{ID: "call_2", Name: toolName2, Input: inputJSON2}},
		{Type: EventToolUseStop, ToolCall: &ToolCall{ID: "call_2", Name: toolName2, Input: inputJSON2}},
		{Type: EventDone, Usage: &Usage{InputTokens: 25, OutputTokens: 15}},
	}
	second := []StreamEvent{
		{Type: EventTextDelta, Text: finalText},
		{Type: EventDone, Usage: &Usage{InputTokens: 35, OutputTokens: 20}},
	}
	return first, second
}

// FakeErrorResponse builds an event sequence that emits an error.
func FakeErrorResponse(err error) []StreamEvent {
	return []StreamEvent{
		{Type: EventError, Error: err},
	}
}

// FakeThinkingTextResponse emits reasoning deltas followed by text. This
// simulates a model that thinks before answering (ollama reasoning_content,
// Anthropic extended thinking, xAI reasoning).
func FakeThinkingTextResponse(thinking, text string, usage *Usage) []StreamEvent {
	if usage == nil {
		usage = &Usage{InputTokens: 10, OutputTokens: 20}
	}
	var events []StreamEvent
	for _, chunk := range splitChunks(thinking, 12) {
		events = append(events, StreamEvent{Type: EventThinkingDelta, Text: chunk})
	}
	events = append(events, StreamEvent{Type: EventTextDelta, Text: text})
	events = append(events, StreamEvent{Type: EventDone, Usage: usage})
	return events
}

// FakeThinkingToolCallResponse emits reasoning, text, and a tool call. Returns
// two sequences: the first for the tool-call turn, the second for the final
// text turn after receiving the tool result.
func FakeThinkingToolCallResponse(thinking, text string, toolName string, toolInput interface{}, finalText string) ([]StreamEvent, []StreamEvent) {
	inputJSON, _ := json.Marshal(toolInput)
	var first []StreamEvent
	for _, chunk := range splitChunks(thinking, 12) {
		first = append(first, StreamEvent{Type: EventThinkingDelta, Text: chunk})
	}
	first = append(first, StreamEvent{Type: EventTextDelta, Text: text})
	first = append(first, StreamEvent{Type: EventToolUseStart, ToolCall: &ToolCall{ID: "call_1", Name: toolName, Input: inputJSON}})
	first = append(first, StreamEvent{Type: EventToolUseStop, ToolCall: &ToolCall{ID: "call_1", Name: toolName, Input: inputJSON}})
	first = append(first, StreamEvent{Type: EventDone, Usage: &Usage{InputTokens: 20, OutputTokens: 15}})
	second := []StreamEvent{
		{Type: EventTextDelta, Text: finalText},
		{Type: EventDone, Usage: &Usage{InputTokens: 30, OutputTokens: 15}},
	}
	return first, second
}

// FakeRedactedThinkingResponse emits a redacted thinking block followed by text.
func FakeRedactedThinkingResponse(text string, usage *Usage) []StreamEvent {
	if usage == nil {
		usage = &Usage{InputTokens: 10, OutputTokens: 10}
	}
	return []StreamEvent{
		{Type: EventThinkingRedacted, Text: "encrypted-sig"},
		{Type: EventTextDelta, Text: text},
		{Type: EventDone, Usage: usage},
	}
}

// splitChunks divides a string into pieces of at most n bytes, simulating
// how a streaming model sends tokens incrementally.
func splitChunks(s string, n int) []string {
	if n < 1 {
		n = 1
	}
	var out []string
	for len(s) > n {
		out = append(out, s[:n])
		s = s[n:]
	}
	if s != "" {
		out = append(out, s)
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}
