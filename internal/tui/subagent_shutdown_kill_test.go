package tui

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/subagent"
	"github.com/mq37/poisson/internal/tools"
)

// spawnLiveSleepingSubagent registers a real *tools.SubagentTool on e's
// registry and spawns one real fake-child process stuck in a long sleep —
// standing in for a subagent mid-run (e.g. its own long bash command) when
// px itself is asked to shut down. Returns the tool so a test can inspect
// live-child count via a fresh subagent_status call if needed.
func spawnLiveSleepingSubagent(t *testing.T, e *tuiIntegEnv) *tools.SubagentTool {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	scriptPath := e.dir + "/fake-child-shutdown-sleep.sh"
	script := "#!/bin/sh\nsleep 30\nprintf '{\"type\":\"done\",\"success\":true}\\n'\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake child script: %v", err)
	}
	restore := subagent.SetLookupExecutableForTest(scriptPath)
	t.Cleanup(restore)

	st := tools.NewSubagentTool(e.dir, func(_, _, _, _, _ string) (bool, string) { return true, "" })
	st.SetRuntime(
		func() string { return "anthropic" },
		func() string { return "claude-opus-5" },
		func() string { return "" },
	)
	e.reg.Register(st)

	res, err := st.Execute(context.Background(), json.RawMessage(`{"task":"do something"}`))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("Execute reported an error: %q", res.Error)
	}
	return st
}

// waitForLiveCount polls until want's live subagent-child count is reached
// or the timeout expires.
func waitForLiveCount(t *testing.T, st *tools.SubagentTool, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if st.ExpediteAll() == want { // ExpediteAll's return count doubles as a live-child probe
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("live subagent count never reached %d within %s", want, timeout)
}

// TestPrepareShutdownLocked_KillsLiveSubagents is the core regression test:
// the shared chokepoint every quit path converges on must force-kill every
// live subagent child, not just cancel the main turn.
func TestPrepareShutdownLocked_KillsLiveSubagents(t *testing.T) {
	e := newTUIIntegEnv(t, nil)
	st := spawnLiveSleepingSubagent(t, e)
	waitForLiveCount(t, st, 1, 2*time.Second)

	e.tui.mu.Lock()
	e.tui.prepareShutdownLocked()
	e.tui.mu.Unlock()

	waitForLiveCount(t, st, 0, 3*time.Second)
}

// TestCtrlDQuit_KillsLiveSubagents drives the REAL Ctrl+D key through
// feedKey (empty buffer -> editor.applyCtrlKey's quit signal ->
// processEditorKey's quit branch -> prepareShutdownLocked), proving that
// specific entry point actually reaches the kill, not just
// prepareShutdownLocked in isolation.
func TestCtrlDQuit_KillsLiveSubagents(t *testing.T) {
	e := newTUIIntegEnv(t, nil)
	st := spawnLiveSleepingSubagent(t, e)
	waitForLiveCount(t, st, 1, 2*time.Second)

	e.tui.mu.Lock()
	e.tui.editor.setText("") // empty buffer is required for Ctrl+D to mean quit, not delete
	e.tui.mu.Unlock()
	// feedKey manages its own locking — must not be called with t.mu already held.
	quit, err := e.tui.feedKey(Key{Kind: KeyCtrl, Byte: 4})
	if err != nil {
		t.Fatalf("feedKey(Ctrl+D) returned an error: %v", err)
	}
	if !quit {
		t.Fatal("feedKey(Ctrl+D) on an empty buffer should signal quit")
	}

	waitForLiveCount(t, st, 0, 3*time.Second)
}

// TestDoubleCtrlCQuit_KillsLiveSubagents drives the REAL Ctrl+C-twice quit
// gesture through feedKey. The first press is simulated as already having
// armed exitArmed/lastCtrlC (the state a prior cancel or Ctrl+C leaves
// behind — see cancelActiveRunLocked and key_dispatch.go's own comment),
// since arming it from scratch depends on unrelated UX state; what matters
// here is that the SECOND press's real dispatch path reaches
// prepareShutdownLocked.
func TestDoubleCtrlCQuit_KillsLiveSubagents(t *testing.T) {
	e := newTUIIntegEnv(t, nil)
	st := spawnLiveSleepingSubagent(t, e)
	waitForLiveCount(t, st, 1, 2*time.Second)

	e.tui.mu.Lock()
	e.tui.exitArmed = true
	e.tui.lastCtrlC = time.Now()
	e.tui.mu.Unlock()
	// feedKey manages its own locking — must not be called with t.mu already held.
	quit, err := e.tui.feedKey(Key{Kind: KeyCtrl, Byte: 3})
	if err != nil {
		t.Fatalf("feedKey(Ctrl+C) returned an error: %v", err)
	}
	if !quit {
		t.Fatal("the second Ctrl+C within the double-tap window should signal quit")
	}

	waitForLiveCount(t, st, 0, 3*time.Second)
}
