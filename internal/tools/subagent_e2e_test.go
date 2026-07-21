package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/subagent"
)

// TestSubagentToolRelaysRetryingStatusEndToEnd exercises the full retry-status
// relay chain this session added across five files (child's ChildEvent
// "retrying" -> SubagentTool.Execute's switch -> reportProgress ->
// progressFn) with a REAL spawned process (a fake shell "child" script, not
// the real px binary) so the actual JSON-over-pipes wiring is what's under
// test, not just each piece in isolation. Zero real LLM calls: the "child"
// never reaches an agent or provider at all.
func TestSubagentToolRelaysRetryingStatusEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	scriptPath := dir + "/fake-child.sh"
	script := `#!/bin/sh
printf '{"type":"tool","tool":"read","turns":1,"contextTokens":100,"contextWindow":200000}\n'
printf '{"type":"retrying","text":"connection lost: dial tcp - reconnecting"}\n'
printf '{"type":"tool","tool":"read","turns":2,"contextTokens":150,"contextWindow":200000}\n'
printf '{"type":"done","success":true,"turns":2,"contextTokens":150,"contextWindow":200000}\n'
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake child script: %v", err)
	}
	restore := subagent.SetLookupExecutableForTest(scriptPath)
	defer restore()

	tool := NewSubagentTool(".", alwaysApproveSubagent)
	tool.SetRuntime(
		func() string { return "anthropic" },
		func() string { return "claude-opus-4-8" },
		func() string { return "" },
	)

	type progressCall struct {
		turns, contextTokens, contextWindow int
		status                              string
	}
	var calls []progressCall
	tool.SetProgressFn(func(toolCallID string, turns, contextTokens, contextWindow int, status string) {
		if toolCallID != "call-1" {
			t.Errorf("toolCallID = %q, want call-1", toolCallID)
		}
		calls = append(calls, progressCall{turns, contextTokens, contextWindow, status})
	})

	ctx := WithToolCallID(context.Background(), "call-1")
	res, err := tool.Execute(ctx, json.RawMessage(`{"task":"do something"}`))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("Execute reported an error: %q (content=%q)", res.Error, res.Content)
	}

	// Expect exactly: turn 1 (status clear), retrying (status set), turn 2
	// (status clear again), final done (status clear) — proving the "clears
	// automatically once real progress resumes" behavior end-to-end, not
	// just at the unit level.
	want := []progressCall{
		{1, 100, 200000, ""},
		{1, 100, 200000, "connection lost: dial tcp - reconnecting"},
		{2, 150, 200000, ""},
		{2, 150, 200000, ""},
	}
	if len(calls) != len(want) {
		t.Fatalf("progressFn calls = %+v, want %+v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Errorf("call %d = %+v, want %+v", i, calls[i], want[i])
		}
	}
}

// TestSubagentToolPropagatesSkillsEnabledFn verifies SetSkillsEnabledFn
// actually reaches the spawned child's argv: when the resolver reports
// skills disabled, the real child process (a fake script, not px) must see
// --no-skills on its own os.Args — the propagation bug this test guards
// against is a resolver wired but silently ignored, or a parent whose
// skills are disabled leaking skills back into every subagent it spawns.
func TestSubagentToolPropagatesSkillsEnabledFn(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	scriptPath := dir + "/fake-child-argv.sh"
	script := `#!/bin/sh
flag=absent
for a in "$@"; do
  if [ "$a" = "--no-skills" ]; then flag=present; fi
done
printf '{"type":"text","text":"flag=%s"}\n' "$flag"
printf '{"type":"done","success":true}\n'
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake child script: %v", err)
	}
	restore := subagent.SetLookupExecutableForTest(scriptPath)
	defer restore()

	for _, tc := range []struct {
		name          string
		skillsEnabled bool
		wantFlag      string
	}{
		{"skills enabled: no flag sent", true, "absent"},
		{"skills disabled: flag propagated", false, "present"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tool := NewSubagentTool(".", alwaysApproveSubagent)
			tool.SetRuntime(
				func() string { return "anthropic" },
				func() string { return "claude-opus-4-8" },
				func() string { return "" },
			)
			tool.SetSkillsEnabledFn(func() bool { return tc.skillsEnabled })

			res, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"do something"}`))
			if err != nil {
				t.Fatalf("Execute returned a Go error: %v", err)
			}
			if res.Error != "" {
				t.Fatalf("Execute reported an error: %q", res.Error)
			}
			want := "flag=" + tc.wantFlag
			if !strings.Contains(res.Content, want) {
				t.Fatalf("child argv result = %q, want it to contain %q", res.Content, want)
			}
		})
	}
}

// TestSubagentToolRelaysRetryingThenChildDiesGivesClearError verifies that if
// the child's connection never recovers (it emits "retrying" and then the
// process simply exits without a "done"), the tool still returns a
// tool-result error instead of hanging or silently succeeding — the
// SubagentTool's caller (the model) needs a clear signal, and the widget
// (via progressFn) should have shown "reconnecting" right up until the end.
func TestSubagentToolRelaysRetryingThenChildDiesGivesClearError(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	scriptPath := dir + "/fake-child-dies.sh"
	script := `#!/bin/sh
printf '{"type":"retrying","text":"connection lost - giving up soon"}\n'
exit 1
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake child script: %v", err)
	}
	restore := subagent.SetLookupExecutableForTest(scriptPath)
	defer restore()

	tool := NewSubagentTool(".", alwaysApproveSubagent)
	tool.SetRuntime(
		func() string { return "anthropic" },
		func() string { return "claude-opus-4-8" },
		func() string { return "" },
	)

	var lastStatus string
	tool.SetProgressFn(func(_ string, _, _, _ int, status string) {
		if status != "" {
			lastStatus = status
		}
	})

	ctx := WithToolCallID(context.Background(), "call-1")
	res, err := tool.Execute(ctx, json.RawMessage(`{"task":"do something"}`))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected a ToolResult.Error when the child died without a done event")
	}
	if lastStatus != "connection lost - giving up soon" {
		t.Errorf("last non-empty progress status = %q, want the retrying text to have been relayed before the child died", lastStatus)
	}
}

// TestSubagentToolRecordsUsageOnDone is the regression test for the bug this
// feature fixes: a subagent's spend used to be computed once inside its own
// ephemeral, throwaway DB and never reach the parent. Verify the usageFn
// callback fires exactly once, with the provider/model the subagent was
// spawned against and the child's final reported usage, and that the
// returned cost is folded into the result text.
func TestSubagentToolRecordsUsageOnDone(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	scriptPath := dir + "/fake-child-usage.sh"
	script := `#!/bin/sh
printf '{"type":"tool","tool":"read","turns":1,"usage":{"InputTokens":100,"OutputTokens":50}}\n'
printf '{"type":"done","success":true,"turns":2,"usage":{"InputTokens":300,"OutputTokens":120,"CacheReadTokens":10,"CacheWriteTokens":5}}\n'
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake child script: %v", err)
	}
	restore := subagent.SetLookupExecutableForTest(scriptPath)
	defer restore()

	tool := NewSubagentTool(".", alwaysApproveSubagent)
	tool.SetRuntime(
		func() string { return "anthropic" },
		func() string { return "claude-opus-4-8" },
		func() string { return "" },
	)

	type call struct {
		providerID, model string
		usage             provider.Usage
	}
	var calls []call
	tool.SetUsageFn(func(providerID, model string, usage *provider.Usage) (float64, error) {
		calls = append(calls, call{providerID, model, *usage})
		return 0.0042, nil
	})

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"do something"}`))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("Execute reported an error: %q", res.Error)
	}

	if len(calls) != 1 {
		t.Fatalf("usageFn called %d times, want exactly 1: %+v", len(calls), calls)
	}
	got := calls[0]
	if got.providerID != "anthropic" || got.model != "claude-opus-4-8" {
		t.Fatalf("usageFn provider/model = %s/%s, want anthropic/claude-opus-4-8", got.providerID, got.model)
	}
	want := provider.Usage{InputTokens: 300, OutputTokens: 120, CacheReadTokens: 10, CacheWriteTokens: 5}
	if got.usage != want {
		t.Fatalf("usageFn usage = %+v, want the final done event's totals %+v (not the earlier tool tick)", got.usage, want)
	}
	if !strings.Contains(res.Content, fmt.Sprintf("$%.4f", 0.0042)) {
		t.Fatalf("result text = %q, want it to contain the recorded cost", res.Content)
	}
}

// TestSubagentToolRecordsUsageFromLastTickWhenCancelled verifies the
// council-flagged gap: a subagent killed by a cancelled parent turn (e.g.
// user hits Esc) skips the "done" event and the normal `done:` label
// entirely (subagent.go's ctx.Done()/ctx.Err() early returns) — but it must
// still get credit for whatever the child had already reported spending as
// of its last progress tick, via the deferred usageFn fallback.
func TestSubagentToolRecordsUsageFromLastTickWhenCancelled(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	scriptPath := dir + "/fake-child-hangs.sh"
	// One usage-bearing tick, then hang well past the test's context timeout
	// (simulating a child mid-flight when the parent's turn is cancelled).
	script := `#!/bin/sh
printf '{"type":"tool","tool":"bash","turns":1,"usage":{"InputTokens":1000,"OutputTokens":200}}\n'
sleep 30
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake child script: %v", err)
	}
	restore := subagent.SetLookupExecutableForTest(scriptPath)
	defer restore()

	tool := NewSubagentTool(".", alwaysApproveSubagent)
	tool.SetRuntime(
		func() string { return "anthropic" },
		func() string { return "claude-opus-4-8" },
		func() string { return "" },
	)

	var calls int
	var gotUsage provider.Usage
	tool.SetUsageFn(func(providerID, model string, usage *provider.Usage) (float64, error) {
		calls++
		gotUsage = *usage
		return 0.01, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	res, err := tool.Execute(ctx, json.RawMessage(`{"task":"do something"}`))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected a ToolResult.Error when the parent turn was cancelled")
	}
	if calls != 1 {
		t.Fatalf("usageFn called %d times, want exactly 1 (via the deferred fallback)", calls)
	}
	want := provider.Usage{InputTokens: 1000, OutputTokens: 200}
	if gotUsage != want {
		t.Fatalf("usageFn usage = %+v, want the last reported tick's totals %+v", gotUsage, want)
	}
}

// TestSubagentToolNoUsageFnIsSafe verifies a tool with no usageFn wired
// (tests that don't care, or any future caller that never binds one) neither
// panics nor appends a bogus cost to the result text.
func TestSubagentToolNoUsageFnIsSafe(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	scriptPath := dir + "/fake-child-nousagefn.sh"
	script := `#!/bin/sh
printf '{"type":"done","success":true,"usage":{"InputTokens":100,"OutputTokens":50}}\n'
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake child script: %v", err)
	}
	restore := subagent.SetLookupExecutableForTest(scriptPath)
	defer restore()

	tool := NewSubagentTool(".", alwaysApproveSubagent)
	tool.SetRuntime(
		func() string { return "anthropic" },
		func() string { return "claude-opus-4-8" },
		func() string { return "" },
	)
	// No SetUsageFn call.

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"do something"}`))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("Execute reported an error: %q", res.Error)
	}
	if strings.Contains(res.Content, "Cost:") {
		t.Fatalf("result text = %q, should not mention a cost with no usageFn wired", res.Content)
	}
}
