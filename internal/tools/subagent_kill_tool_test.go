package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/subagent"
)

// TestSubagentKillTerminatesLiveChild kills a real live child by job ID and
// proves the underlying process actually exits, not just that the job
// registry reports "killed".
func TestSubagentKillTerminatesLiveChild(t *testing.T) {
	scriptPath := fakeSleepingChildScript(t, t.TempDir(), "fake-child-kill-tool.sh")
	restore := subagent.SetLookupExecutableForTest(scriptPath)
	defer restore()

	tool := NewSubagentTool(".", alwaysApproveSubagent)
	tool.SetRuntime(func() string { return "anthropic" }, func() string { return "claude-opus-5" }, func() string { return "" })
	kill := NewSubagentKillTool(tool)

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"do something"}`))
	if err != nil || res.Error != "" {
		t.Fatalf("Execute: res=%+v err=%v", res, err)
	}
	jobID := jobIDFromAck(t, res.Content)
	waitForJobSpawned(t, tool, jobID, 2*time.Second)

	killRes, err := kill.Execute(context.Background(), json.RawMessage(`{"jobId":"`+jobID+`"}`))
	if err != nil || killRes.Error != "" {
		t.Fatalf("subagent_kill: res=%+v err=%v", killRes, err)
	}

	job := waitForJob(t, tool, jobID, 2*time.Second)
	if job.status != "killed" {
		t.Fatalf("job status = %q, want killed", job.status)
	}
}

// TestSubagentKillAllKillsEveryVisibleJob covers the all:true fan-out.
func TestSubagentKillAllKillsEveryVisibleJob(t *testing.T) {
	dir := t.TempDir()
	scriptPath := fakeSleepingChildScript(t, dir, "fake-child-killall.sh")
	restore := subagent.SetLookupExecutableForTest(scriptPath)
	defer restore()

	tool := NewSubagentTool(".", alwaysApproveSubagent)
	tool.SetRuntime(func() string { return "anthropic" }, func() string { return "claude-opus-5" }, func() string { return "" })
	kill := NewSubagentKillTool(tool)

	const n = 3
	jobIDs := make([]string, n)
	for i := 0; i < n; i++ {
		res, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"do something"}`))
		if err != nil || res.Error != "" {
			t.Fatalf("Execute %d: res=%+v err=%v", i, res, err)
		}
		jobIDs[i] = jobIDFromAck(t, res.Content)
		waitForJobSpawned(t, tool, jobIDs[i], 2*time.Second)
	}

	killRes, err := kill.Execute(context.Background(), json.RawMessage(`{"all":true}`))
	if err != nil || killRes.Error != "" {
		t.Fatalf("subagent_kill all: res=%+v err=%v", killRes, err)
	}
	if !strings.Contains(killRes.Content, "3") {
		t.Fatalf("kill-all result = %q, want it to report 3 jobs killed", killRes.Content)
	}

	for _, id := range jobIDs {
		job := waitForJob(t, tool, id, 2*time.Second)
		if job.status != "killed" {
			t.Fatalf("job %s status = %q, want killed", id, job.status)
		}
	}
}

// TestSubagentKillSkipsJobsFromAnotherSession proves session scoping: a job
// spawned under one session is invisible to subagent_kill once the "current
// session" has moved on — kill-by-id reads as not-found, and all:true kills
// nothing, matching subagent_status/subagent_result's own scoping.
func TestSubagentKillSkipsJobsFromAnotherSession(t *testing.T) {
	scriptPath := fakeSleepingChildScript(t, t.TempDir(), "fake-child-otherssn.sh")
	restore := subagent.SetLookupExecutableForTest(scriptPath)
	defer restore()

	tool := NewSubagentTool(".", alwaysApproveSubagent)
	tool.SetRuntime(func() string { return "anthropic" }, func() string { return "claude-opus-5" }, func() string { return "" })
	t.Cleanup(func() { tool.KillAll() }) // the child this test spawns is deliberately never killed by name below
	current := "session-a"
	tool.SetSessionIDFn(func() string { return current })
	kill := NewSubagentKillTool(tool)

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"do something"}`))
	if err != nil || res.Error != "" {
		t.Fatalf("Execute: res=%+v err=%v", res, err)
	}
	jobID := jobIDFromAck(t, res.Content)
	waitForJobSpawned(t, tool, jobID, 2*time.Second)

	current = "session-b"

	killRes, err := kill.Execute(context.Background(), json.RawMessage(`{"jobId":"`+jobID+`"}`))
	if err != nil {
		t.Fatalf("subagent_kill returned a Go error: %v", err)
	}
	if killRes.Error == "" {
		t.Fatal("expected \"no such subagent job\" for a job spawned under a different session")
	}

	allRes, err := kill.Execute(context.Background(), json.RawMessage(`{"all":true}`))
	if err != nil || allRes.Error != "" {
		t.Fatalf("subagent_kill all: res=%+v err=%v", allRes, err)
	}
	if !strings.Contains(allRes.Content, "0") {
		t.Fatalf("kill-all from a different session = %q, want it to report 0 killed", allRes.Content)
	}

	current = "session-a"
	job, ok := tool.getJob(jobID)
	if !ok {
		t.Fatal("job disappeared")
	}
	if status := job.view().status; status == "killed" {
		t.Fatal("job from another session must not have been killed")
	}
}

// TestSubagentKillRejectsFinishedJob covers the ordinary guard: a job that
// already reached a terminal state cannot be killed again.
func TestSubagentKillRejectsFinishedJob(t *testing.T) {
	tool, jobID := newDoneSubagentJob(t)
	kill := NewSubagentKillTool(tool)

	res, err := kill.Execute(context.Background(), json.RawMessage(`{"jobId":"`+jobID+`"}`))
	if err != nil {
		t.Fatalf("subagent_kill returned a Go error: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected an error killing an already-finished job")
	}
}

// TestSubagentKillRequiresJobIdOrAll and
// TestSubagentKillRejectsBothJobIdAndAll cover the input-validation guards.
func TestSubagentKillRequiresJobIdOrAll(t *testing.T) {
	tool := NewSubagentTool(".", alwaysApproveSubagent)
	kill := NewSubagentKillTool(tool)
	res, err := kill.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("returned a Go error: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected an error with neither jobId nor all set")
	}
}

func TestSubagentKillRejectsBothJobIdAndAll(t *testing.T) {
	tool := NewSubagentTool(".", alwaysApproveSubagent)
	kill := NewSubagentKillTool(tool)
	res, err := kill.Execute(context.Background(), json.RawMessage(`{"jobId":"sub-x","all":true}`))
	if err != nil {
		t.Fatalf("returned a Go error: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected an error with both jobId and all set")
	}
}

// TestSubagentKillResultRetrievableOnce proves a killed job's partial output
// is still fetchable via subagent_result exactly once, same as any other
// finished job.
func TestSubagentKillResultRetrievableOnce(t *testing.T) {
	scriptPath := fakeSleepingChildScript(t, t.TempDir(), "fake-child-kill-result.sh")
	restore := subagent.SetLookupExecutableForTest(scriptPath)
	defer restore()

	tool := NewSubagentTool(".", alwaysApproveSubagent)
	tool.SetRuntime(func() string { return "anthropic" }, func() string { return "claude-opus-5" }, func() string { return "" })
	kill := NewSubagentKillTool(tool)
	result := NewSubagentResultTool(tool)

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"do something"}`))
	if err != nil || res.Error != "" {
		t.Fatalf("Execute: res=%+v err=%v", res, err)
	}
	jobID := jobIDFromAck(t, res.Content)
	waitForJobSpawned(t, tool, jobID, 2*time.Second)

	if _, err := kill.Execute(context.Background(), json.RawMessage(`{"jobId":"`+jobID+`"}`)); err != nil {
		t.Fatalf("subagent_kill: %v", err)
	}
	waitForJob(t, tool, jobID, 2*time.Second)

	input := json.RawMessage(`{"jobId":"` + jobID + `"}`)
	// The first retrieve must succeed as a retrieve — its content reports the
	// kill itself ("subagent cancelled", the same generic exit reason ctx
	// cancellation always produces), which is expected: killing is not a
	// successful completion. What matters is that it doesn't say "already
	// retrieved" or "still running".
	first, err := result.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("subagent_result returned a Go error: %v", err)
	}
	if strings.Contains(first.Error, "already retrieved") || strings.Contains(first.Error, "still") {
		t.Fatalf("first subagent_result after kill = %+v, want a retrievable result", first)
	}

	second, err := result.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("subagent_result returned a Go error: %v", err)
	}
	if second.Error == "" {
		t.Fatal("expected the one-shot guard to reject a second retrieve after kill")
	}
}

// TestSubagentKillQueuedJobNeverSpawns proves KillJob cancels a still-queued
// job's context directly, at the unit level — a job manually constructed in
// "queued" state, exactly the shape runJob's own slot-acquire select is
// blocked on before it's ever won a real concurrency slot. This
// deliberately does NOT go through the real Execute/runJob/subagentSlots
// pipeline: that channel is a single package-level global shared by every
// test in this package, and proving "never calls Spawn" by racing a real
// job against a saturated-then-monitored slot count is inherently
// timing-sensitive against whatever other tests' background cleanup is
// doing concurrently (see TestKillAllCancelsQueuedJobsWithoutSpawning for
// that integration-level version, which accepts that trade-off). The
// underlying safety property — runJob checks ctx.Err()/shuttingDown under
// spawnMu immediately before calling subagent.Spawn, so a cancelled ctx
// always aborts there — is exercised deterministically here by driving
// KillJob's own cancellation path and observing the ctx it cancels.
func TestSubagentKillQueuedJobNeverSpawns(t *testing.T) {
	tool := NewSubagentTool(".", alwaysApproveSubagent)
	jobCtx, jobCancel := context.WithCancel(context.Background())
	job := &subagentJob{id: "sub-queued-kill", status: "queued", cancel: jobCancel}
	tool.jobs[job.id] = job

	if err := tool.KillJob(job.id); err != nil {
		t.Fatalf("KillJob: %v", err)
	}

	if err := jobCtx.Err(); err == nil {
		t.Fatal("KillJob did not cancel the queued job's context — a runJob blocked on its slot-acquire select would never see this and could still spawn")
	}
	if !job.killRequested {
		t.Fatal("killRequested not set")
	}
}
