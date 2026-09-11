package tools

import (
	"fmt"
	"testing"
	"time"
)

// newTerminalJob builds a bare done/error subagentJob directly in the
// registry for pruning tests — bypassing Execute/runJob entirely, since
// pruneJobsLocked only cares about status/doneAt/retrieved.
func newTerminalJobFor(t *SubagentTool, id, status string, doneAt time.Time, retrieved bool) {
	t.jobs[id] = &subagentJob{
		id: id, startedAt: doneAt, status: status, doneAt: doneAt, retrieved: retrieved,
		result: ToolResult{Content: "some transcript"},
	}
}

// TestRetrieveJobFreesResultPayload is the regression guard for bug 6's
// other half: the transcript itself (the big allocation) is dropped the
// moment it's handed out, not just marked retrieved.
func TestRetrieveJobFreesResultPayload(t *testing.T) {
	tool := NewSubagentTool(".", alwaysApproveSubagent)
	job := &subagentJob{id: "sub-1", status: "done", result: ToolResult{Content: "the whole transcript"}}
	tool.jobs[job.id] = job

	got := tool.retrieveJob(job)
	if got.Content != "the whole transcript" {
		t.Fatalf("first retrieve content = %q, want the full result", got.Content)
	}
	if job.result.Content != "" {
		t.Fatalf("job.result.Content after retrieve = %q, want cleared", job.result.Content)
	}

	second := tool.retrieveJob(job)
	if second.Error == "" {
		t.Fatal("expected the one-shot guard to reject a second retrieve")
	}
}

// TestPruneJobsNeverEvictsRunningJobs proves queued/running jobs are never
// touched regardless of age or count.
func TestPruneJobsNeverEvictsRunningJobs(t *testing.T) {
	tool := NewSubagentTool(".", alwaysApproveSubagent)
	old := time.Now().Add(-2 * jobRetention)
	tool.jobs["sub-running"] = &subagentJob{id: "sub-running", status: "running", startedAt: old}
	tool.jobs["sub-queued"] = &subagentJob{id: "sub-queued", status: "queued", startedAt: old}

	tool.jobsMu.Lock()
	tool.pruneJobsLocked()
	tool.jobsMu.Unlock()

	if _, ok := tool.jobs["sub-running"]; !ok {
		t.Fatal("running job evicted")
	}
	if _, ok := tool.jobs["sub-queued"]; !ok {
		t.Fatal("queued job evicted")
	}
}

// TestPruneJobsEvictsRetrievedAndStaleTerminalJobs covers both eviction
// triggers under the cap: already-retrieved, or past jobRetention — and
// proves a fresh, not-yet-retrieved terminal job survives.
func TestPruneJobsEvictsRetrievedAndStaleTerminalJobs(t *testing.T) {
	tool := NewSubagentTool(".", alwaysApproveSubagent)
	now := time.Now()
	newTerminalJobFor(tool, "sub-retrieved", "done", now, true)
	newTerminalJobFor(tool, "sub-stale", "error", now.Add(-2*jobRetention), false)
	newTerminalJobFor(tool, "sub-fresh-unretrieved", "done", now, false)

	tool.jobsMu.Lock()
	tool.pruneJobsLocked()
	tool.jobsMu.Unlock()

	if _, ok := tool.jobs["sub-retrieved"]; ok {
		t.Error("already-retrieved terminal job not evicted")
	}
	if _, ok := tool.jobs["sub-stale"]; ok {
		t.Error("stale (past jobRetention) terminal job not evicted")
	}
	if _, ok := tool.jobs["sub-fresh-unretrieved"]; !ok {
		t.Error("fresh, not-yet-retrieved terminal job evicted too early")
	}
}

// TestPruneJobsRespectsMaxTrackedJobs covers the hard ceiling: a burst of
// many fresh, unretrieved terminal jobs (none individually eligible under
// jobRetention) still gets trimmed back to maxTrackedJobs, oldest first.
func TestPruneJobsRespectsMaxTrackedJobs(t *testing.T) {
	tool := NewSubagentTool(".", alwaysApproveSubagent)
	base := time.Now()
	const n = maxTrackedJobs + 10
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("sub-burst-%d", i)
		newTerminalJobFor(tool, id, "done", base.Add(time.Duration(i)*time.Second), false)
	}

	tool.jobsMu.Lock()
	tool.pruneJobsLocked()
	tool.jobsMu.Unlock()

	if len(tool.jobs) > maxTrackedJobs {
		t.Fatalf("len(jobs) = %d after prune, want <= %d", len(tool.jobs), maxTrackedJobs)
	}
	// The oldest ones (lowest i, earliest doneAt) must be the ones gone.
	if _, ok := tool.jobs["sub-burst-0"]; ok {
		t.Error("oldest job in the burst should have been evicted first")
	}
}
