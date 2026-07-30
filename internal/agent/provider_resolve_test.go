package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/tools"
)

// fakeProviderTool is a minimal tools.Tool that also implements
// tools.DefaultProviderResolver — a stand-in for web_ask/web_search/fetch
// that doesn't touch the network, so the round-trip through the real agent
// turn loop (stream -> provider injection -> persistence) can be tested fast
// and deterministically.
type fakeProviderTool struct {
	name    string
	backend string

	lastInput json.RawMessage // captured for assertions — see TestExecuteReceivesUnresolvedInput
}

func (f *fakeProviderTool) Name() string                   { return f.name }
func (f *fakeProviderTool) Description() string            { return "test tool" }
func (f *fakeProviderTool) Schema() json.RawMessage        { return json.RawMessage(`{"type":"object"}`) }
func (f *fakeProviderTool) ResolveDefaultProvider() string { return f.backend }
func (f *fakeProviderTool) Execute(_ context.Context, input json.RawMessage) (tools.ToolResult, error) {
	f.lastInput = append(json.RawMessage(nil), input...)
	return tools.ToolResult{Content: "ok"}, nil
}

// lastToolUseInput runs a Prompt through the agent with fp's scripted
// responses, then returns the ToolInput bytes of the first tool_use block
// persisted in the assistant message.
func lastToolUseInput(t *testing.T, reg *tools.Registry, first, second []provider.StreamEvent) json.RawMessage {
	t.Helper()
	s := newTestStore(t)
	sessionID := newTestSession(t, s, "test-model")
	cfg := newTestConfig()
	p := newFakeProvider()
	p.SetResponses([][]provider.StreamEvent{first, second})

	ch := make(chan OutputEvent, 256)
	drainEvents(ch)
	a := NewAgent(s, p, reg, cfg, sessionID, ch, nil)
	if err := a.Prompt("go"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	close(ch)

	msgs, err := s.GetMessages(sessionID)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	for _, m := range msgs {
		if m.Role != "assistant" {
			continue
		}
		var blocks []contentBlockJSON
		if json.Unmarshal([]byte(m.Content), &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			if b.Type == "tool_use" {
				return b.ToolInput
			}
		}
	}
	t.Fatalf("no tool_use block found in %d messages", len(msgs))
	return nil
}

// TestOmittedProviderResolvedToDefault: a model that calls a tool with no
// explicit "provider" must still have the persisted (and TUI-visible)
// tool_use input carry the backend that actually ran — not an empty field
// that reads as "no backend chosen".
func TestOmittedProviderResolvedToDefault(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&fakeProviderTool{name: "web_ask", backend: "grok"})

	first, second := provider.FakeToolCallResponse("web_ask",
		map[string]interface{}{"query": "why"}, "because")
	got := lastToolUseInput(t, reg, first, second)

	var in struct {
		Provider string `json:"provider"`
	}
	if err := json.Unmarshal(got, &in); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if in.Provider != "grok" {
		t.Errorf("provider = %q, want %q (input: %s)", in.Provider, "grok", got)
	}
}

// TestExplicitProviderNotOverwritten: an explicit "provider" the model did
// send must survive untouched — injection only fills gaps, never overrides.
func TestExplicitProviderNotOverwritten(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&fakeProviderTool{name: "web_ask", backend: "grok"})

	first, second := provider.FakeToolCallResponse("web_ask",
		map[string]interface{}{"query": "why", "provider": "exa"}, "because")
	got := lastToolUseInput(t, reg, first, second)

	var in struct {
		Provider string `json:"provider"`
	}
	if err := json.Unmarshal(got, &in); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if in.Provider != "exa" {
		t.Errorf("provider = %q, want %q (explicit value overwritten)", in.Provider, "exa")
	}
}

// TestExecuteReceivesUnresolvedInput: display-side provider resolution must
// never leak into what Execute actually runs on. Some tools (web_ask) tell
// an explicit "provider" the model typed apart from a self-picked default by
// checking their own input JSON's "provider" field — web_ask falls back
// grok -> exa only when IT chose grok as the default, not when the model
// demanded grok specifically. If agent.go pre-filled "provider":"grok" into
// the dispatched input, Execute could no longer tell the two apart and would
// treat every resolved-default call as an explicit request, turning a
// graceful fallback into a hard failure on any transient backend hiccup.
// This asserts Execute still sees the call exactly as the model sent it —
// only the persisted/displayed copy carries the resolved value.
func TestExecuteReceivesUnresolvedInput(t *testing.T) {
	reg := tools.NewRegistry()
	tool := &fakeProviderTool{name: "web_ask", backend: "grok"}
	reg.Register(tool)

	first, second := provider.FakeToolCallResponse("web_ask",
		map[string]interface{}{"query": "why"}, "because")
	got := lastToolUseInput(t, reg, first, second)

	// Persisted/displayed copy: resolved.
	var persisted struct {
		Provider string `json:"provider"`
	}
	if err := json.Unmarshal(got, &persisted); err != nil {
		t.Fatalf("unmarshal persisted: %v", err)
	}
	if persisted.Provider != "grok" {
		t.Fatalf("persisted provider = %q, want grok", persisted.Provider)
	}

	// What Execute actually received: unresolved, exactly as the model sent it.
	if tool.lastInput == nil {
		t.Fatalf("Execute was never called")
	}
	var dispatched struct {
		Provider string `json:"provider"`
	}
	if err := json.Unmarshal(tool.lastInput, &dispatched); err != nil {
		t.Fatalf("unmarshal dispatched: %v", err)
	}
	if dispatched.Provider != "" {
		t.Errorf("Execute received provider = %q, want empty (unresolved) — display resolution leaked into dispatch", dispatched.Provider)
	}
}

// TestBatchNestedProviderResolvedToDefault: the same default-resolution has
// to reach into a batch call's nested calls too — batch is tool-agnostic, so
// it can't do this itself, and skipping it here would leave a nested
// web_ask/web_search/fetch card silently missing its backend forever.
func TestBatchNestedProviderResolvedToDefault(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&fakeProviderTool{name: "web_ask", backend: "grok"})
	reg.Register(tools.NewBatchTool(reg))

	batchInput := map[string]interface{}{
		"calls": []map[string]interface{}{
			{"tool": "web_ask", "input": map[string]interface{}{"query": "why"}},
		},
	}
	first, second := provider.FakeToolCallResponse("batch", batchInput, "because")
	got := lastToolUseInput(t, reg, first, second)

	var parsed struct {
		Calls []struct {
			Tool  string `json:"tool"`
			Input struct {
				Provider string `json:"provider"`
			} `json:"input"`
		} `json:"calls"`
	}
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("unmarshal: %v (input: %s)", err, got)
	}
	if len(parsed.Calls) != 1 || parsed.Calls[0].Input.Provider != "grok" {
		t.Errorf("batch input = %s, want nested call to carry provider=grok", got)
	}
}
