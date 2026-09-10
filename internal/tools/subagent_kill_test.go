package tools

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/subagent"
)

// fakeSleepingChildScript writes a fake "child" that just sleeps, standing
// in for a subagent stuck mid-run (e.g. a long bash command) when px itself
// is shutting down.
func fakeSleepingChildScript(t *testing.T, dir, name string) string {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	path := dir + "/" + name
	script := "#!/bin/sh\nsleep 30\nprintf '{\"type\":\"done\",\"success\":true}\\n'\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake child script: %v", err)
	}
	return path
}

// TestKillAllTerminatesEveryLiveChild is KillAll's fan-out regression guard,
// mirroring TestExpediteAllSignalsEveryLiveChild: spawns THREE real fake
// child processes each stuck in a long sleep, tracks them as live (same
// mechanism a real Execute call uses), then asserts KillAll both reports
// killing all 3 AND that every single one of them actually exited — not
// just the first or the last, and not left hanging as an orphan.
func TestKillAllTerminatesEveryLiveChild(t *testing.T) {
	dir := t.TempDir()
	scriptPath := fakeSleepingChildScript(t, dir, "fake-child-sleep.sh")
	restore := subagent.SetLookupExecutableForTest(scriptPath)
	defer restore()

	tool := NewSubagentTool(".", alwaysApproveSubagent)

	const n = 3
	children := make([]*subagent.ChildProcess, n)
	for i := 0; i < n; i++ {
		c, err := subagent.Spawn(subagent.SpawnInput{
			Task:      "do something",
			Cwd:       ".",
			SessionID: "sess-kill-fanout",
		})
		if err != nil {
			t.Fatalf("Spawn child %d: %v", i, err)
		}
		children[i] = c
		tool.trackLive(c)
	}

	got := tool.KillAll()
	if got != n {
		t.Fatalf("KillAll() = %d, want %d (one of the %d live children was not signalled)", got, n, n)
	}

	for i, c := range children {
		done := make(chan error, 1)
		go func() { done <- c.Wait() }()
		select {
		case <-done:
			// Exited — killed successfully. Wait() returning an error (e.g.
			// "signal: killed") is expected and fine; a hang is the failure.
		case <-time.After(3 * time.Second):
			t.Errorf("child %d did not exit within 3s of KillAll — orphaned instead of killed", i)
		}
	}
}

// TestKillAllZeroWhenNoLiveChildren covers the empty-map baseline: a freshly
// constructed SubagentTool with nothing tracked kills nobody and returns 0,
// not a panic.
func TestKillAllZeroWhenNoLiveChildren(t *testing.T) {
	tool := NewSubagentTool(".", alwaysApproveSubagent)
	if got := tool.KillAll(); got != 0 {
		t.Fatalf("KillAll() = %d, want 0 (no live children tracked)", got)
	}
}
