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

// TestBackgroundContextCancelTerminatesRunningJob proves the process-wide
// background context (wired via SetBackgroundContext / main.go's
// BindSubagentBackgroundContext) actually reaches a running job: cancelling
// it must terminate the job promptly instead of leaving it orphaned to run
// past process shutdown. This is the only automatic way a job now ends
// before it finishes on its own — jobs have no per-job time limit (see
// jobCtx's doc comment in subagent.go's Execute) — besides an explicit
// action=kill.
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
