package tools

import (
	"regexp"
	"testing"
	"time"
)

// jobIDFromAckRe extracts the job ID from the subagent tool's spawn-ack
// text ("...spawned as job sub-xxxx. ...") — see Execute's ack format.
var jobIDFromAckRe = regexp.MustCompile(`spawned as job (\S+)\.`)

// jobIDFromAck extracts the job ID a successful async spawn's ack mentions,
// failing the test if the text doesn't look like one.
func jobIDFromAck(t *testing.T, content string) string {
	t.Helper()
	m := jobIDFromAckRe.FindStringSubmatch(content)
	if m == nil {
		t.Fatalf("ack text has no job id: %q", content)
	}
	return m[1]
}

// waitForJob polls tool's job registry until jobID reaches a terminal state
// (done/error), or fails the test after timeout — the async replacement for
// what used to be Execute's own blocking return.
func waitForJob(t *testing.T, tool *SubagentTool, jobID string, timeout time.Duration) subagentJobView {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		job, ok := tool.getJob(jobID)
		if !ok {
			t.Fatalf("job %s not found in registry", jobID)
		}
		v := job.view()
		if v.status == "done" || v.status == "error" {
			return v
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s did not finish within %s (status=%s)", jobID, timeout, v.status)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// waitForJobSpawned polls until jobID's child process has actually been
// spawned (status flips to "running" only after subagent.Spawn succeeds —
// see runJob), or reaches a terminal state first. Needed before a test
// returns and tears down its SetLookupExecutableForTest fixture: Spawn now
// happens inside a background goroutine, so a test that returns the instant
// Execute's ack comes back races that goroutine — its deferred restore()
// can revert lookupExecutable to the real binary before Spawn ever runs.
func waitForJobSpawned(t *testing.T, tool *SubagentTool, jobID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		job, ok := tool.getJob(jobID)
		if !ok {
			t.Fatalf("job %s not found in registry", jobID)
		}
		switch job.view().status {
		case "running", "done", "error":
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s never reached running within %s", jobID, timeout)
		}
		time.Sleep(2 * time.Millisecond)
	}
}
