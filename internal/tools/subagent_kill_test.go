package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"sync"
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

// saturateSubagentSlots fills the package-level subagentSlots channel to
// capacity with fake acquisitions and keeps it topped up for the rest of the
// test — lets a test force a job into "queued behind a full concurrency
// pool" without actually spawning maxConcurrentSubagents real child
// processes. subagentSlots is process-wide, shared with every other test in
// this package; a still-finishing background job from an EARLIER test can
// release a real slot at any moment (its own runJob goroutine isn't
// necessarily done just because that test function already returned), which
// would otherwise let the job under test slip through and actually spawn
// instead of staying queued — a background top-up goroutine (stopped via
// t.Cleanup) closes that window by re-claiming any slot that frees up for as
// long as this test is running.
func saturateSubagentSlots(t *testing.T) {
	t.Helper()
	stop := make(chan struct{})
	var mu sync.Mutex
	held := 0
	fill := func() {
		for {
			select {
			case subagentSlots <- struct{}{}:
				mu.Lock()
				held++
				mu.Unlock()
			default:
				return
			}
		}
	}
	fill()
	done := make(chan struct{})
	go func() {
		defer close(done)
		// Tight spin-yield loop, not a ticker: any gap here is a real
		// window where another test's job releasing a slot could let the
		// job under test slip through and actually spawn before this
		// goroutine gets a chance to reclaim it. runtime.Gosched() keeps
		// this from starving other goroutines while still checking far
		// more often than a multi-millisecond ticker would.
		for {
			select {
			case <-stop:
				return
			default:
				fill()
				runtime.Gosched()
			}
		}
	}()
	t.Cleanup(func() {
		close(stop)
		<-done
		mu.Lock()
		n := held
		mu.Unlock()
		for i := 0; i < n; i++ {
			<-subagentSlots
		}
	})
}

// TestKillAllCancelsQueuedJobsWithoutSpawning is the regression guard for
// bug 8: a job still queued behind a full concurrency pool (never reached
// subagent.Spawn) used to be invisible to KillAll, which iterates only
// t.live. Killing the live children then freed a slot and let the queued
// job immediately win it and spawn a brand-new child process while the
// parent was already exiting, orphaned with nothing left to reap it.
// KillAll now cancels every non-terminal job's own context (see job.cancel,
// set at Execute time) BEFORE touching t.live, so a job still waiting on
// the slot select sees that cancellation and exits there — this proves the
// underlying script (which would touch a marker file if ever actually
// spawned) never runs.
func TestKillAllCancelsQueuedJobsWithoutSpawning(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	markerPath := dir + "/spawned.marker"
	scriptPath := dir + "/fake-child-marks-spawn.sh"
	script := "#!/bin/sh\ntouch " + markerPath + "\nprintf '{\"type\":\"done\",\"success\":true}\\n'\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake child script: %v", err)
	}
	restore := subagent.SetLookupExecutableForTest(scriptPath)
	defer restore()

	saturateSubagentSlots(t)

	tool := NewSubagentTool(".", alwaysApproveSubagent)
	tool.SetRuntime(func() string { return "anthropic" }, func() string { return "claude-opus-5" }, func() string { return "" })

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"do something"}`))
	if err != nil || res.Error != "" {
		t.Fatalf("Execute: res=%+v err=%v", res, err)
	}
	jobID := jobIDFromAck(t, res.Content)

	// Give runJob a moment to actually reach the slot-acquire select.
	time.Sleep(50 * time.Millisecond)
	if job, ok := tool.getJob(jobID); !ok || job.view().status != "queued" {
		t.Fatalf("job should still be queued (slots saturated) before KillAll")
	}

	if n := tool.KillAll(); n != 0 {
		t.Fatalf("KillAll() = %d, want 0 (no live children — only a queued one)", n)
	}

	job := waitForJob(t, tool, jobID, 2*time.Second)
	if job.status != "error" {
		t.Fatalf("queued job status = %q, want error (cancelled without ever spawning)", job.status)
	}
	if _, err := os.Stat(markerPath); err == nil {
		t.Fatal("marker file exists — the queued job's script ran despite KillAll, meaning it spawned an orphan")
	}
}

// TestSweepStaleTempDBsRemovesOldFiles and TestSweepStaleTempDBsLeavesFreshFiles
// cover bug 9: a subagent scratch DB abandoned by a SIGKILLed/crashed
// process (whose own deferred removeDBFiles never ran) is cleaned up by the
// NEXT process to start, based on age alone — never touching a file young
// enough to belong to a still-legitimately-running job.
func TestSweepStaleTempDBsRemovesOldFiles(t *testing.T) {
	dir := t.TempDir()
	oldPath := dir + "/poisson-sub-old12345.db"
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.WriteFile(oldPath+suffix, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-48 * time.Hour)
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Chtimes(oldPath+suffix, old, old); err != nil {
			t.Fatal(err)
		}
	}

	sweepStaleTempDBsIn(dir)

	for _, suffix := range []string{"", "-wal", "-shm"} {
		if _, err := os.Stat(oldPath + suffix); !os.IsNotExist(err) {
			t.Fatalf("%s still exists after sweep", oldPath+suffix)
		}
	}
}

func TestSweepStaleTempDBsLeavesFreshFiles(t *testing.T) {
	dir := t.TempDir()
	freshPath := dir + "/poisson-sub-fresh67890.db"
	if err := os.WriteFile(freshPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	unrelatedPath := dir + "/poisson-sub-old12345.db.wal-unrelated-name"
	if err := os.WriteFile(unrelatedPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	sweepStaleTempDBsIn(dir)

	if _, err := os.Stat(freshPath); err != nil {
		t.Fatalf("fresh file was removed by sweep: %v", err)
	}
	if _, err := os.Stat(unrelatedPath); err != nil {
		t.Fatalf("unrelated file was removed by sweep: %v", err)
	}
}
