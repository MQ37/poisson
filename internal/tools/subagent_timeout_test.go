package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/subagent"
)

// withShortSubagentJobTimeout shrinks the package var for the duration of
// one test, restoring it after — same seam agent.midStreamErrorBackoff
// tests use.
func withShortSubagentJobTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := subagentJobTimeout
	subagentJobTimeout = d
	t.Cleanup(func() { subagentJobTimeout = old })
}

// TestSubagentJobTimeoutCancelsWedgedChild is the regression guard for bug
// 7: before SubagentTool.bgCtx was ever wired to anything but
// context.Background() in production, a wedged child had no cancellation
// path short of the whole process exiting — this proves a job whose child
// never produces a "done" event is force-terminated once its per-job
// timeout elapses, instead of hanging forever.
func TestSubagentJobTimeoutCancelsWedgedChild(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	withShortSubagentJobTimeout(t, 100*time.Millisecond)

	dir := t.TempDir()
	scriptPath := dir + "/fake-child-wedged.sh"
	// Never emits a "done" event — simulates a wedged provider connection.
	script := "#!/bin/sh\nsleep 30\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake child script: %v", err)
	}
	restore := subagent.SetLookupExecutableForTest(scriptPath)
	defer restore()

	tool := NewSubagentTool(".", alwaysApproveSubagent)
	tool.SetRuntime(func() string { return "anthropic" }, func() string { return "claude-opus-5" }, func() string { return "" })

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"do something"}`))
	if err != nil || res.Error != "" {
		t.Fatalf("Execute: res=%+v err=%v", res, err)
	}
	jobID := jobIDFromAck(t, res.Content)

	job := waitForJob(t, tool, jobID, 2*time.Second)
	if job.status != "error" {
		t.Fatalf("job status = %q, want error (timed out)", job.status)
	}
	if job.result.Error == "" {
		t.Fatal("expected a non-empty error on the timed-out job's result")
	}

	// The slot must actually be released — not just the job marked done —
	// or a run of timed-out jobs still exhausts maxConcurrentSubagents.
	select {
	case subagentSlots <- struct{}{}:
		<-subagentSlots
	default:
		t.Fatal("subagent slot not released after the job's timeout")
	}
}

// TestBackgroundContextCancelTerminatesRunningJob proves the process-wide
// background context (wired via SetBackgroundContext / main.go's
// BindSubagentBackgroundContext) actually reaches a running job: cancelling
// it must terminate the job promptly instead of leaving it orphaned to run
// past process shutdown.
func TestBackgroundContextCancelTerminatesRunningJob(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	scriptPath := dir + "/fake-child-long.sh"
	script := "#!/bin/sh\nsleep 30\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake child script: %v", err)
	}
	restore := subagent.SetLookupExecutableForTest(scriptPath)
	defer restore()

	tool := NewSubagentTool(".", alwaysApproveSubagent)
	tool.SetRuntime(func() string { return "anthropic" }, func() string { return "claude-opus-5" }, func() string { return "" })
	bgCtx, cancelBg := context.WithCancel(context.Background())
	tool.SetBackgroundContext(bgCtx)

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"do something"}`))
	if err != nil || res.Error != "" {
		t.Fatalf("Execute: res=%+v err=%v", res, err)
	}
	jobID := jobIDFromAck(t, res.Content)
	waitForJobSpawned(t, tool, jobID, 2*time.Second)

	cancelBg() // exactly what main.go's cancelJobs() does on process shutdown

	job := waitForJob(t, tool, jobID, 2*time.Second)
	if job.status != "error" {
		t.Fatalf("job status = %q, want error (cancelled by shutdown)", job.status)
	}
}
