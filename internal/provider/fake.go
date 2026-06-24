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

// CallCount returns the number of times Stream was called.
func (p *FakeProvider) CallCount() int { return p.callCount }

// Stream returns a channel that emits the pre-programmed events for the
// next response in the sequence. If responses are exhausted, it emits a
// single EventDone with zero usage.
func (p *FakeProvider) Stream(ctx context.Context, req *Request) (<-chan StreamEvent, error) {
	p.callCount++
	p.lastRequest = req

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

// FakeErrorResponse builds an event sequence that emits an error.
func FakeErrorResponse(err error) []StreamEvent {
	return []StreamEvent{
		{Type: EventError, Error: err},
	}
}
