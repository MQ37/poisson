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

// spawnJobUnderSession spawns a real (fake-child) job with the tool's
// sessionIDFn pinned to session, returning the job id once the job has
// finished.
func spawnJobUnderSession(t *testing.T, tool *SubagentTool, session string) string {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	scriptPath := dir + "/fake-child-session.sh"
	script := "#!/bin/sh\nprintf '{\"type\":\"done\",\"success\":true}\\n'\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake child script: %v", err)
	}
	restore := subagent.SetLookupExecutableForTest(scriptPath)
	t.Cleanup(restore)

	current := session
	tool.SetSessionIDFn(func() string { return current })
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"do something"}`))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("Execute reported an error: %q", res.Error)
	}
	jobID := jobIDFromAck(t, res.Content)
	waitForJob(t, tool, jobID, 2*time.Second)
	return jobID
}

func newSessionScopedTool(t *testing.T) *SubagentTool {
	tool := NewSubagentTool(".", alwaysApproveSubagent)
	tool.SetRuntime(
		func() string { return "anthropic" },
		func() string { return "claude-opus-5" },
		func() string { return "" },
	)
	return tool
}

func TestSubagentSession_JobInvisibleAfterSessionSwitch(t *testing.T) {
	tool := newSessionScopedTool(t)
	jobID := spawnJobUnderSession(t, tool, "s-session-a")

	// Simulate /new or /resume switching the live session away from the one
	// that spawned the job — SubagentTool is process-lifetime, shared
	// across every session the user switches through.
	tool.SetSessionIDFn(func() string { return "s-session-b" })

	if _, ok := tool.getJob(jobID); ok {
		t.Fatal("getJob found a job spawned under a different session")
	}
	for _, j := range tool.listJobs() {
		if j.id == jobID {
			t.Fatal("listJobs included a job spawned under a different session")
		}
	}
}

func TestSubagentSession_JobVisibleFromSpawningSession(t *testing.T) {
	tool := newSessionScopedTool(t)
	jobID := spawnJobUnderSession(t, tool, "s-session-a")

	if _, ok := tool.getJob(jobID); !ok {
		t.Fatal("getJob did not find the job from its own spawning session")
	}
	found := false
	for _, j := range tool.listJobs() {
		if j.id == jobID {
			found = true
		}
	}
	if !found {
		t.Fatal("listJobs did not include the job from its own spawning session")
	}
}

// TestSubagentSession_UnwiredResolverFailsOpen is the backward-compatibility
// guarantee: every Phase 1/2 test constructs a SubagentTool with no
// SetSessionIDFn call at all — session scoping must not start hiding jobs
// just because nothing wired a resolver.
func TestSubagentSession_UnwiredResolverFailsOpen(t *testing.T) {
	tool := newSessionScopedTool(t)
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	scriptPath := dir + "/fake-child-unscoped.sh"
	script := "#!/bin/sh\nprintf '{\"type\":\"done\",\"success\":true}\\n'\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake child script: %v", err)
	}
	restore := subagent.SetLookupExecutableForTest(scriptPath)
	defer restore()

	// No SetSessionIDFn call.
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"do something"}`))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	jobID := jobIDFromAck(t, res.Content)
	waitForJob(t, tool, jobID, 2*time.Second)

	if _, ok := tool.getJob(jobID); !ok {
		t.Fatal("getJob hid a job when no session resolver was ever wired — should fail open")
	}
}

// TestSubagentSession_JobDoneFnReceivesSpawningSessionID proves the id
// carried to jobDoneFn is the one that was live at SPAWN time, not
// whatever's live when the job later finishes — Agent.CompleteSubagentJob
// needs the ORIGINAL session to compare against the current one.
func TestSubagentSession_JobDoneFnReceivesSpawningSessionID(t *testing.T) {
	tool := newSessionScopedTool(t)
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	scriptPath := dir + "/fake-child-donefn-session.sh"
	script := "#!/bin/sh\nprintf '{\"type\":\"done\",\"success\":true}\\n'\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake child script: %v", err)
	}
	restore := subagent.SetLookupExecutableForTest(scriptPath)
	defer restore()

	current := "s-spawning-session"
	tool.SetSessionIDFn(func() string { return current })

	var gotSessionID string
	done := make(chan struct{})
	tool.SetJobDoneFn(func(jobID, sessionID, toolCallID string, res ToolResult) {
		gotSessionID = sessionID
		close(done)
	})

	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"do something"}`)); err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}

	// The "live" session changes before the job actually finishes — the
	// callback must still report the session that SPAWNED it. Synchronize
	// via the jobDoneFn channel itself, not waitForJob/getJob: those are now
	// session-scoped to whatever "current" is (see the tests above), which
	// is deliberately no longer the spawning session by this point.
	current = "s-different-session-now"

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("jobDoneFn was never called")
	}
	if gotSessionID != "s-spawning-session" {
		t.Fatalf("jobDoneFn sessionID = %q, want the spawning session %q", gotSessionID, "s-spawning-session")
	}
}
