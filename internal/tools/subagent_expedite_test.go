package tools

import (
	"os"
	"os/exec"
	"testing"

	"github.com/mq37/poisson/internal/subagent"
)

// fakeExpediteAckScript writes a fake "child" that blocks on a single stdin
// read, then reports (via a "tool_result" event) whether the line it
// received was the expedite shape — a real spawned process, not a mock,
// mirroring the fake-child-process pattern already used throughout this
// package's other *_e2e_test.go files (subagent.SetLookupExecutableForTest
// pointed at a small shell script written with os.WriteFile + the
// executable bit).
func fakeExpediteAckScript(t *testing.T, dir, name string) string {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	path := dir + "/" + name
	script := `#!/bin/sh
read -r line
if echo "$line" | grep -q '"type":"expedite"'; then
  printf '{"type":"tool_result","result":"expedited"}\n'
else
  printf '{"type":"tool_result","result":"other"}\n'
fi
printf '{"type":"done","success":true}\n'
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake child script: %v", err)
	}
	return path
}

// TestExpediteAllSignalsEveryLiveChild is the fan-out regression guard for
// SubagentTool.ExpediteAll: spawns THREE real fake child processes (real
// exec'd shell scripts via subagent.Spawn, real stdin/stdout pipes), tracks
// all three as live via the tool's own trackLive (the same mechanism a real
// Execute call uses — see Execute's t.trackLive(child)/defer t.untrackLive),
// then asserts ExpediteAll both reports signalling all 3 AND that every
// single one of them actually observed the expedite shape on its own stdin —
// not just the first or the last. A loop that silently only reached one
// child (e.g. an early return, or a truncated fan-out) would still report
// SendExpedite succeeded for that one child and could slip past a test that
// only checked the returned count or only checked one child.
func TestExpediteAllSignalsEveryLiveChild(t *testing.T) {
	dir := t.TempDir()
	scriptPath := fakeExpediteAckScript(t, dir, "fake-child.sh")
	restore := subagent.SetLookupExecutableForTest(scriptPath)
	defer restore()

	tool := NewSubagentTool(".", alwaysApproveSubagent)

	const n = 3
	children := make([]*subagent.ChildProcess, n)
	for i := 0; i < n; i++ {
		c, err := subagent.Spawn(subagent.SpawnInput{
			Task:      "do something",
			Cwd:       ".",
			SessionID: "sess-expedite-fanout",
		})
		if err != nil {
			t.Fatalf("Spawn child %d: %v", i, err)
		}
		defer c.Reap()
		children[i] = c
		tool.trackLive(c)
	}

	got := tool.ExpediteAll()
	if got != n {
		t.Fatalf("ExpediteAll() = %d, want %d (one of the %d live children was not signalled)", got, n, n)
	}

	for i, c := range children {
		ev, err := c.ReadEvent()
		if err != nil || ev == nil || ev.Type != "tool_result" {
			t.Fatalf("child %d ReadEvent (tool_result) = %+v, err=%v", i, ev, err)
		}
		if ev.Result != "expedited" {
			t.Errorf("child %d saw result %q, want %q — this child never actually received the expedite signal despite ExpediteAll's count", i, ev.Result, "expedited")
		}
	}
}

// TestExpediteAllZeroWhenNoLiveChildren covers the empty-map baseline: a
// freshly constructed SubagentTool with nothing tracked signals nobody and
// returns 0, not a panic or a stale count.
func TestExpediteAllZeroWhenNoLiveChildren(t *testing.T) {
	tool := NewSubagentTool(".", alwaysApproveSubagent)
	if got := tool.ExpediteAll(); got != 0 {
		t.Fatalf("ExpediteAll() = %d, want 0 (no live children tracked)", got)
	}
}
