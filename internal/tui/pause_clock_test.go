package tui

import (
	"context"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/agent"
)

// resetApprovalClock isolates tests from each other's paused time.
func resetApprovalClock(t *testing.T) {
	t.Helper()
	prev := approvalClock
	approvalClock = &pauseClock{}
	t.Cleanup(func() { approvalClock = prev })
}

// TestBlockElapsedExcludesApprovalWait is the bug: a bash command parked at an
// approval prompt kept its card timer running, so a 40ms command that waited
// two minutes for a human reported the two minutes.
func TestBlockElapsedExcludesApprovalWait(t *testing.T) {
	resetApprovalClock(t)

	var m BlockMeta
	markStarted(&m)
	m.StartedAt = time.Now().Add(-500 * time.Millisecond) // pretend 500ms of real work

	approvalClock.begin()
	approvalClock.since = time.Now().Add(-5 * time.Second) // 5s parked at the prompt
	approvalClock.end()

	got := blockElapsedMs(m)
	if got > 1500 {
		t.Fatalf("elapsed = %dms, want ~500ms (approval wait must not count)", got)
	}
}

// TestElapsedFrozenWhileApprovalOpen covers the live case: the timer must stop
// advancing while the prompt is still up, not only get corrected afterwards.
func TestElapsedFrozenWhileApprovalOpen(t *testing.T) {
	resetApprovalClock(t)

	var m BlockMeta
	markStarted(&m)

	approvalClock.begin()
	defer approvalClock.end()

	first := blockElapsedMs(m)
	time.Sleep(30 * time.Millisecond)
	second := blockElapsedMs(m)

	if second-first > 10 {
		t.Fatalf("elapsed advanced %dms while an approval was open, want frozen", second-first)
	}
}

// TestPauseBaselineIsPerBlock guards the baseline: a block started AFTER an
// earlier approval must not have that earlier wait deducted from its own
// elapsed time.
func TestPauseBaselineIsPerBlock(t *testing.T) {
	resetApprovalClock(t)

	// An approval happened earlier in the session.
	approvalClock.begin()
	approvalClock.since = time.Now().Add(-10 * time.Second)
	approvalClock.end()

	var m BlockMeta
	markStarted(&m)
	m.StartedAt = time.Now().Add(-2 * time.Second)

	got := blockElapsedMs(m)
	if got < 1500 {
		t.Fatalf("elapsed = %dms, want ~2000ms (an earlier approval is not this block's wait)", got)
	}
}

// TestApproveFreezesBlockTimers is the wiring test: pausing the clock is
// pointless if TUI.Approve stops calling it, and the unit tests above would
// still pass in that case. Drives a real Approve() and asserts a live tool
// card's elapsed time does not grow while the prompt waits. Covers both
// approval kinds — bash risk gates and file/edit approvals both land here
// (cmd/px/main.go routes fileApprovalFn to this same method).
func TestApproveFreezesBlockTimers(t *testing.T) {
	resetApprovalClock(t)
	tui := newTestTUIHelper()

	tui.scroll.appendToolCall(1, "call-1", "bash", []byte(`{"command":"sleep 0"}`))
	live := &tui.scroll.blocks[tui.scroll.blockCount()-1]

	result := make(chan bool, 1)
	go func() {
		allowed, _ := tui.Approve(context.Background(), "rm -rf x", "danger", "", agent.BashRiskHigh, agent.ApprovalOriginMain)
		result <- allowed
	}()

	deadline := time.Now().Add(time.Second)
	for !tui.approving.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !tui.approving.Load() {
		t.Fatal("Approve never entered approving state")
	}

	first := blockElapsedMs(live.meta)
	time.Sleep(40 * time.Millisecond)
	if second := blockElapsedMs(live.meta); second-first > 15 {
		t.Errorf("tool card timer advanced %dms while the approval prompt was up, want frozen", second-first)
	}

	tui.approvalAnswer <- approvalReply{Allowed: true}
	select {
	case <-result:
	case <-time.After(time.Second):
		t.Fatal("Approve timed out")
	}

	// And the wait must not land in the card's final duration either.
	tui.scroll.completeToolCall("call-1", "ok", "", "", 0)
	if got := live.meta.DurationMs; got > 100 {
		t.Errorf("final DurationMs = %dms, want the approval wait excluded", got)
	}
}

// TestNestedPauseSharesOneWindow covers a /btw approval opening on top of a
// main-turn approval: the inner end must not restart the clock while the outer
// prompt is still waiting.
func TestNestedPauseSharesOneWindow(t *testing.T) {
	resetApprovalClock(t)

	approvalClock.begin()
	approvalClock.begin()
	approvalClock.end()

	var m BlockMeta
	markStarted(&m)
	first := blockElapsedMs(m)
	time.Sleep(30 * time.Millisecond)
	if second := blockElapsedMs(m); second-first > 10 {
		t.Fatalf("elapsed advanced %dms with the outer approval still open", second-first)
	}
	approvalClock.end()
}
