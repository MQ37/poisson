package main

import (
	"path/filepath"
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
