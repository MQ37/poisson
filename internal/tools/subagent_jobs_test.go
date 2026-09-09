package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/subagent"
)

// newRunningSubagentTool wires a SubagentTool against a fake child process
// that finishes almost immediately, returning the tool and the spawned
// job's ID (parsed from Execute's ack).
func newDoneSubagentJob(t *testing.T) (*SubagentTool, string) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	scriptPath := dir + "/fake-child-status.sh"
	script := `#!/bin/sh
printf '{"type":"done","success":true,"turns":1,"contextTokens":10,"contextWindow":200000}\n'
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake child script: %v", err)
	}
	restore := subagent.SetLookupExecutableForTest(scriptPath)
	t.Cleanup(restore)

	tool := NewSubagentTool(".", alwaysApproveSubagent)
	tool.SetRuntime(
		func() string { return "anthropic" },
		func() string { return "claude-opus-5" },
		func() string { return "" },
	)
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"do something"}`))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("Execute reported an error: %q", res.Error)
	}
	jobID := jobIDFromAck(t, res.Content)
	waitForJob(t, tool, jobID, 2*time.Second)
	return tool, jobID
}

// TestSubagentExecuteReturnsImmediately is the core async claim: Execute
// returns before the child even needs to have started, well under the time
// a real (even trivial) child process takes to spawn and exit.
func TestSubagentExecuteReturnsImmediately(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	scriptPath := dir + "/fake-child-slow.sh"
	script := `#!/bin/sh
sleep 1
printf '{"type":"done","success":true}\n'
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake child script: %v", err)
	}
	restore := subagent.SetLookupExecutableForTest(scriptPath)
	defer restore()

	tool := NewSubagentTool(".", alwaysApproveSubagent)
	tool.SetRuntime(
		func() string { return "anthropic" },
		func() string { return "claude-opus-5" },
		func() string { return "" },
	)

	start := time.Now()
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"do something"}`))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("Execute reported an error: %q", res.Error)
	}
	if !strings.Contains(res.Content, "spawned as job") {
		t.Fatalf("ack text = %q, want it to mention a spawned job", res.Content)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("Execute took %v, want well under the child's own 1s sleep — it should not block on the child at all", elapsed)
	}
	waitForJob(t, tool, jobIDFromAck(t, res.Content), 2*time.Second)
}

// TestSubagentJobSurvivesSpawningTurnCtxCancellation is the regression test
// for docs/async-subagent-plan.md §A: internal/tui/agent_io.go's startTurn
// cancels its turn ctx unconditionally the instant the spawning
// Prompt/Execute call returns. A job whose background work depended on that
// ctx would be killed within microseconds of its own spawn ack — this
// proves runJob is genuinely on a separate, longer-lived context instead.
func TestSubagentJobSurvivesSpawningTurnCtxCancellation(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	scriptPath := dir + "/fake-child-outlives-turn.sh"
	script := `#!/bin/sh
sleep 0.2
printf '{"type":"done","success":true}\n'
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake child script: %v", err)
	}
	restore := subagent.SetLookupExecutableForTest(scriptPath)
	defer restore()

	tool := NewSubagentTool(".", alwaysApproveSubagent)
	tool.SetRuntime(
		func() string { return "anthropic" },
		func() string { return "claude-opus-5" },
		func() string { return "" },
	)

	// Mirrors startTurn: a ctx that's cancelled the instant the call using
	// it returns — Execute's own synchronous prefix uses it, but the
	// spawned job must not.
	turnCtx, cancelTurn := context.WithCancel(context.Background())
	res, err := tool.Execute(turnCtx, json.RawMessage(`{"task":"do something"}`))
	cancelTurn() // exactly what startTurn's deferred cleanup does right after Prompt returns
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("Execute reported an error: %q", res.Error)
	}

	job := waitForJob(t, tool, jobIDFromAck(t, res.Content), 2*time.Second)
	if job.result.Error != "" {
		t.Fatalf("job reported an error after its spawning turn's ctx was cancelled: %q", job.result.Error)
	}
	if !strings.Contains(job.result.Content, "Subagent finished") {
		t.Fatalf("job result = %q, want it to have actually run to completion", job.result.Content)
	}
}

// --- subagent_status ---

func TestSubagentStatus_NoJobs(t *testing.T) {
	tool := NewSubagentTool(".", alwaysApproveSubagent)
	status := NewSubagentStatusTool(tool)
	res, err := status.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if !strings.Contains(res.Content, "no subagent jobs") {
		t.Fatalf("content = %q, want a no-jobs message", res.Content)
	}
}

func TestSubagentStatus_UnknownJobID(t *testing.T) {
	tool := NewSubagentTool(".", alwaysApproveSubagent)
	status := NewSubagentStatusTool(tool)
	res, err := status.Execute(context.Background(), json.RawMessage(`{"jobId":"sub-nonexistent"}`))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if !strings.Contains(res.Error, "no such subagent job") {
		t.Fatalf("error = %q, want a not-found message", res.Error)
	}
}

func TestSubagentStatus_ListsAndDescribesFinishedJob(t *testing.T) {
	tool, jobID := newDoneSubagentJob(t)
	status := NewSubagentStatusTool(tool)

	listRes, err := status.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if !strings.Contains(listRes.Content, jobID) || !strings.Contains(listRes.Content, "done") {
		t.Fatalf("list content = %q, want it to mention job %s as done", listRes.Content, jobID)
	}
	if !strings.Contains(listRes.Content, "ready — call subagent_result") {
		t.Fatalf("list content = %q, want the ready-to-retrieve hint before retrieval", listRes.Content)
	}

	oneRes, err := status.Execute(context.Background(), mustJSON(t, map[string]string{"jobId": jobID}))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if !strings.Contains(oneRes.Content, "status: done") {
		t.Fatalf("single-job content = %q, want it to report status: done", oneRes.Content)
	}
}

// --- subagent_result ---

func TestSubagentResult_UnknownJobID(t *testing.T) {
	tool := NewSubagentTool(".", alwaysApproveSubagent)
	result := NewSubagentResultTool(tool)
	res, err := result.Execute(context.Background(), json.RawMessage(`{"jobId":"sub-nonexistent"}`))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if !strings.Contains(res.Error, "no such subagent job") {
		t.Fatalf("error = %q, want a not-found message", res.Error)
	}
}

func TestSubagentResult_MissingJobID(t *testing.T) {
	tool := NewSubagentTool(".", alwaysApproveSubagent)
	result := NewSubagentResultTool(tool)
	res, err := result.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if !strings.Contains(res.Error, "jobId is required") {
		t.Fatalf("error = %q, want a jobId-required message", res.Error)
	}
}

func TestSubagentResult_StillRunningErrorsWithoutBlocking(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	scriptPath := dir + "/fake-child-slow-result.sh"
	script := `#!/bin/sh
sleep 5
printf '{"type":"done","success":true}\n'
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake child script: %v", err)
	}
	restore := subagent.SetLookupExecutableForTest(scriptPath)
	defer restore()

	tool := NewSubagentTool(".", alwaysApproveSubagent)
	tool.SetRuntime(
		func() string { return "anthropic" },
		func() string { return "claude-opus-5" },
		func() string { return "" },
	)
	spawnRes, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"do something"}`))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	jobID := jobIDFromAck(t, spawnRes.Content)
	// Ensure the child process actually exists before this test returns and
	// tears down the SetLookupExecutableForTest fixture — otherwise the
	// background goroutine's own Spawn call can race the fixture's teardown.
	waitForJobSpawned(t, tool, jobID, 2*time.Second)

	result := NewSubagentResultTool(tool)
	start := time.Now()
	res, err := result.Execute(context.Background(), mustJSON(t, map[string]string{"jobId": jobID}))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if !strings.Contains(res.Error, "still") {
		t.Fatalf("error = %q, want a still-running message", res.Error)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("subagent_result took %v, want it to return immediately instead of blocking on the running job", elapsed)
	}
}

func TestSubagentResult_RetrievesOnceThenErrors(t *testing.T) {
	tool, jobID := newDoneSubagentJob(t)
	result := NewSubagentResultTool(tool)

	first, err := result.Execute(context.Background(), mustJSON(t, map[string]string{"jobId": jobID}))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if first.Error != "" {
		t.Fatalf("first retrieval reported an error: %q", first.Error)
	}
	if !strings.Contains(first.Content, "Subagent finished") {
		t.Fatalf("first retrieval content = %q, want the full result text", first.Content)
	}

	second, err := result.Execute(context.Background(), mustJSON(t, map[string]string{"jobId": jobID}))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if !strings.Contains(second.Error, "already retrieved") {
		t.Fatalf("second retrieval = %+v, want an already-retrieved error", second)
	}

	// subagent_status must still describe the job as retrieved, not
	// silently forget it happened.
	status := NewSubagentStatusTool(tool)
	statusRes, err := status.Execute(context.Background(), mustJSON(t, map[string]string{"jobId": jobID}))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if !strings.Contains(statusRes.Content, "already retrieved") {
		t.Fatalf("status after retrieval = %q, want it to mention already retrieved", statusRes.Content)
	}
}
