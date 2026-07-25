package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/agent"
	"github.com/mq37/poisson/internal/config"
	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/store"
	"github.com/mq37/poisson/internal/testutil"
	"github.com/mq37/poisson/internal/tools"
)

// TestForwardChildEvents_* cover the child-mode wire protocol every
// subagent's parent depends on (cmd/px was previously the least-tested
// package in the repo: forwardChildEvents used to be inlined directly in
// runChildMode, which also parses os.Args, opens a real store, and builds a
// real provider from disk config/auth — making it untestable as a whole
// without a real LLM call. Extracting the translation loop into a pure
// function lets it be driven here with a FakeProvider: zero network, zero
// real API calls, exactly what the rest of the suite already does.

func newChildTestAgent(t *testing.T, fp *provider.FakeProvider, outputChan chan agent.OutputEvent) *agent.Agent {
	t.Helper()
	dir := testutil.TempDir(t)
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.CreateSession(&store.Session{ID: "s1", Cwd: ".", Provider: "fake", Model: "test-model"}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	reg := tools.NewRegistry()
	cfg := &config.Config{Provider: config.ProviderConfig{Default: "fake"}}
	a := agent.NewAgent(st, fp, reg, cfg, "s1", outputChan, nil)
	a.SetModel("test-model")
	return a
}

func TestForwardChildEvents_TextAndToolStart(t *testing.T) {
	fp := provider.NewFakeProvider("fake", []provider.Model{{ID: "test-model", Name: "T", ContextWindow: 200000}})
	outputChan := make(chan agent.OutputEvent, 8)
	a := newChildTestAgent(t, fp, outputChan)

	var got []map[string]interface{}
	write := func(ev map[string]interface{}) { got = append(got, ev) }

	outputChan <- agent.OutputEvent{Type: agent.OutputText, Text: "hello"}
	outputChan <- agent.OutputEvent{Type: agent.OutputToolStart, ToolName: "read", ToolInput: []byte(`{"path":"x"}`)}
	close(outputChan)

	toolCount := forwardChildEvents(outputChan, a, write)

	if toolCount != 1 {
		t.Fatalf("toolCount = %d, want 1", toolCount)
	}
	if len(got) != 2 {
		t.Fatalf("events = %d, want 2: %+v", len(got), got)
	}
	if got[0]["type"] != "text" || got[0]["text"] != "hello" {
		t.Errorf("event 0 = %+v, want text/hello", got[0])
	}
	if got[1]["type"] != "tool" || got[1]["tool"] != "read" {
		t.Errorf("event 1 = %+v, want tool/read", got[1])
	}
	if got[1]["turns"] == nil || got[1]["contextTokens"] == nil || got[1]["contextWindow"] == nil {
		t.Errorf("tool event missing turns/context fields: %+v", got[1])
	}
}

// TestForwardChildEvents_Retrying verifies agent.OutputRetrying is relayed
// as a "retrying" child event carrying its text verbatim — the exact shape
// the parent's SubagentTool.Execute relay (internal/tools/subagent.go)
// depends on to show "reconnecting" on the widget.
func TestForwardChildEvents_Retrying(t *testing.T) {
	fp := provider.NewFakeProvider("fake", []provider.Model{{ID: "test-model", Name: "T", ContextWindow: 200000}})
	outputChan := make(chan agent.OutputEvent, 8)
	a := newChildTestAgent(t, fp, outputChan)

	var got []map[string]interface{}
	write := func(ev map[string]interface{}) { got = append(got, ev) }

	outputChan <- agent.OutputEvent{Type: agent.OutputRetrying, Text: "connection lost: dial tcp: refused — reconnecting…"}
	outputChan <- agent.OutputEvent{Type: agent.OutputRetrying, Text: "reconnected — resuming"}
	close(outputChan)

	forwardChildEvents(outputChan, a, write)

	if len(got) != 2 {
		t.Fatalf("events = %d, want 2: %+v", len(got), got)
	}
	for i, want := range []string{"connection lost: dial tcp: refused — reconnecting…", "reconnected — resuming"} {
		if got[i]["type"] != "retrying" {
			t.Errorf("event %d type = %v, want retrying", i, got[i]["type"])
		}
		if got[i]["text"] != want {
			t.Errorf("event %d text = %v, want %q", i, got[i]["text"], want)
		}
	}
}

// TestForwardChildEvents_ToolResultWithAndWithoutError verifies the "error"
// key is present only when the tool actually failed — the parent relay
// switches on its presence, not just its value, so an accidentally-always-
// present empty "error" key would be indistinguishable from a real failure
// to a naive `if payload["error"] != nil` check downstream.
func TestForwardChildEvents_ToolResultWithAndWithoutError(t *testing.T) {
	fp := provider.NewFakeProvider("fake", []provider.Model{{ID: "test-model", Name: "T", ContextWindow: 200000}})
	outputChan := make(chan agent.OutputEvent, 8)
	a := newChildTestAgent(t, fp, outputChan)

	var got []map[string]interface{}
	write := func(ev map[string]interface{}) { got = append(got, ev) }

	outputChan <- agent.OutputEvent{Type: agent.OutputToolResult, ToolName: "read", ToolResultContent: "file contents"}
	outputChan <- agent.OutputEvent{Type: agent.OutputToolResult, ToolName: "bash", ToolError: "exit code 1"}
	close(outputChan)

	forwardChildEvents(outputChan, a, write)

	if len(got) != 2 {
		t.Fatalf("events = %d, want 2: %+v", len(got), got)
	}
	if _, ok := got[0]["error"]; ok {
		t.Errorf("successful tool_result should have no error key: %+v", got[0])
	}
	if got[0]["result"] != "file contents" {
		t.Errorf("result = %v, want %q", got[0]["result"], "file contents")
	}
	if got[1]["error"] != "exit code 1" {
		t.Errorf("error = %v, want %q", got[1]["error"], "exit code 1")
	}
}

// TestForwardChildEvents_UnhandledEventTypeIsIgnored guards the switch's
// default (no-op) behavior: OutputApproval/OutputCompacting/etc. are
// deliberately not forwarded (the child's internal steps besides
// text/tool/retrying never reach the parent) — a stray write for one of
// these would leak protocol noise into the parent's relay.
// TestForwardChildEvents_ToolStartCarriesRealCumulativeUsage is the
// integration test for the subagent-cost-rollup wire protocol: it runs a
// REAL turn through a REAL Agent (PromptWithContext, backed by a
// FakeProvider so no network call happens) so a.CumulativeUsage() reflects
// actually-recorded api_calls rows, then verifies the "tool" event
// forwardChildEvents emits afterward carries that exact usage on the wire —
// not a hand-authored fixture, the genuine child-mode code path
// (cmd/px/main.go) that SubagentTool.Execute's usageFn callback ultimately
// depends on. The other subagent-cost tests either hand-write the "usage"
// JSON in a fake shell script (internal/tools/subagent_e2e_test.go) or call
// Agent.RecordSubagentUsage directly (internal/agent/subagent_usage_test.go)
// — neither exercises whether THIS translation layer computes the number
// correctly from real recorded usage.
func TestForwardChildEvents_ToolStartCarriesRealCumulativeUsage(t *testing.T) {
	// FakeProvider — no network, no real API call (see provider/fake.go's
	// doc comment: "never makes real HTTP calls").
	fp := provider.NewFakeProvider("fake", []provider.Model{{ID: "test-model", Name: "T", ContextWindow: 200000}})
	fp.SetResponses([][]provider.StreamEvent{
		provider.FakeTextResponse("answer", &provider.Usage{InputTokens: 123, OutputTokens: 45, CacheReadTokens: 6}),
	})
	outputChan := make(chan agent.OutputEvent, 8)
	a := newChildTestAgent(t, fp, outputChan)

	// Drain the real turn's own events concurrently — exactly as runChildMode
	// does via its own forwardChildEvents goroutine — so PromptWithContext
	// can never block on a full channel regardless of how many events one
	// turn produces.
	drained := make(chan struct{})
	go func() {
		for range outputChan {
		}
		close(drained)
	}()

	// Run a real turn so a.CumulativeUsage() reflects an actually-recorded
	// api_calls row, exactly as it would inside runChildMode before the
	// child's next tool call fires a "tool" progress event.
	if err := a.PromptWithContext(context.Background(), "do something"); err != nil {
		t.Fatalf("PromptWithContext: %v", err)
	}
	close(outputChan)
	<-drained

	wantUsage := a.CumulativeUsage()
	if wantUsage.InputTokens != 123 || wantUsage.OutputTokens != 45 || wantUsage.CacheReadTokens != 6 {
		t.Fatalf("CumulativeUsage() after the turn = %+v, want it to reflect the recorded usage", wantUsage)
	}

	// Isolated channel + write capture to inspect exactly what
	// forwardChildEvents attaches to a fresh tool event now that the agent
	// has real recorded usage.
	toolChan := make(chan agent.OutputEvent, 1)
	toolChan <- agent.OutputEvent{Type: agent.OutputToolStart, ToolName: "read"}
	close(toolChan)
	var got []map[string]interface{}
	write := func(ev map[string]interface{}) { got = append(got, ev) }
	forwardChildEvents(toolChan, a, write)

	if len(got) != 1 || got[0]["type"] != "tool" {
		t.Fatalf("events = %+v, want exactly one tool event", got)
	}
	usage, ok := got[0]["usage"].(provider.Usage)
	if !ok {
		t.Fatalf("tool event usage = %#v (%T), want a provider.Usage", got[0]["usage"], got[0]["usage"])
	}
	if usage != wantUsage {
		t.Fatalf("tool event usage = %+v, want the agent's real CumulativeUsage() %+v", usage, wantUsage)
	}
}

// TestRecoverChildPanicEmitsErrorEventWithLabelAndStack covers the panic-
// recovery wiring added to runChildMode (both the main-run and the
// event-forwarding goroutine each install their own `defer recover()` that
// calls this): before it existed, any panic in a subagent's own run crashed
// the child process bare — the parent's ReadEvent() only ever saw its
// stdout pipe close and reported an opaque "EOF" with no way to tell a
// panic from a network blip from anything else. This verifies the emitted
// event actually carries the label, the panic value, and a real stack trace
// (not just the bare message) so a future crash is diagnosable.
func TestRecoverChildPanicEmitsErrorEventWithLabelAndStack(t *testing.T) {
	var got []map[string]interface{}
	write := func(ev map[string]interface{}) { got = append(got, ev) }

	func() {
		defer func() {
			if r := recover(); r != nil {
				recoverChildPanic(write, "run", r)
			}
		}()
		panic("boom: nil pointer somewhere deep in a tool")
	}()

	if len(got) != 1 {
		t.Fatalf("events = %+v, want exactly one error event", got)
	}
	if got[0]["type"] != "error" {
		t.Fatalf("type = %v, want error", got[0]["type"])
	}
	errText, _ := got[0]["error"].(string)
	if !strings.Contains(errText, "subagent run panicked: boom: nil pointer somewhere deep in a tool") {
		t.Fatalf("error text missing label/panic value: %q", errText)
	}
	// debug.Stack() output always starts with "goroutine " — confirms a real
	// stack trace is attached, not just the bare panic message.
	if !strings.Contains(errText, "goroutine ") {
		t.Fatalf("error text missing a stack trace: %q", errText)
	}
}

// TestRecoverChildPanicDistinguishesLabels verifies the main-run and
// event-forwarding goroutines are distinguishable in the emitted error —
// they panic independently and each installs its own recover, so the label
// is the only way to tell which one actually crashed.
func TestRecoverChildPanicDistinguishesLabels(t *testing.T) {
	var got map[string]interface{}
	write := func(ev map[string]interface{}) { got = ev }

	recoverChildPanic(write, "event-forwarding", "boom")

	errText, _ := got["error"].(string)
	if !strings.Contains(errText, "subagent event-forwarding panicked: boom") {
		t.Fatalf("error text missing event-forwarding label: %q", errText)
	}
}

func TestForwardChildEvents_UnhandledEventTypeIsIgnored(t *testing.T) {
	fp := provider.NewFakeProvider("fake", []provider.Model{{ID: "test-model", Name: "T", ContextWindow: 200000}})
	outputChan := make(chan agent.OutputEvent, 8)
	a := newChildTestAgent(t, fp, outputChan)

	var got []map[string]interface{}
	write := func(ev map[string]interface{}) { got = append(got, ev) }

	outputChan <- agent.OutputEvent{Type: agent.OutputCompacting, Text: "compacting..."}
	outputChan <- agent.OutputEvent{Type: agent.OutputApproval}
	close(outputChan)

	forwardChildEvents(outputChan, a, write)

	if len(got) != 0 {
		t.Fatalf("events = %+v, want none forwarded", got)
	}
}
