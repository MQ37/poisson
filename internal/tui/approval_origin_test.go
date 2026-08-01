package tui

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/agent"
)

// TestApprove_BTWOriginKeepsOverlayAliveAndRestoresIt is the direct
// regression test for /btw + bash coexistence: an approval prompt firing
// while /btw's own panel is showing must not cancel its underlying
// quick-answer stream, must show as the approval overlay while pending, and
// must restore the exact same btwOverlay once answered.
func TestApprove_BTWOriginKeepsOverlayAliveAndRestoresIt(t *testing.T) {
	tui := newTestTUIHelper()
	btw := newBTWOverlay("what's in this file?")
	var cancelled atomic.Bool
	btw.setCancel(func() { cancelled.Store(true) })
	tui.mu.Lock()
	tui.activeOverlay = btw
	tui.currentBTW = btw // openBTW invariant — see TUI.currentBTW
	tui.mu.Unlock()

	result := make(chan bool, 1)
	go func() {
		allowed, _ := tui.Approve(context.Background(), "rm -rf /tmp/x", "cleanup", "/tmp", agent.BashRiskHigh, agent.ApprovalOriginBTW)
		result <- allowed
	}()

	deadline := time.Now().Add(500 * time.Millisecond)
	for !tui.approving.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !tui.approving.Load() {
		t.Fatal("Approve never entered approving state")
	}

	tui.mu.Lock()
	_, isApproval := tui.activeOverlay.(*approvalOverlay)
	tui.mu.Unlock()
	if !isApproval {
		t.Fatal("expected the approval overlay to be showing while pending")
	}

	tui.approvalAnswer <- approvalReply{Allowed: true}
	select {
	case got := <-result:
		if !got {
			t.Fatal("expected allow")
		}
	case <-time.After(time.Second):
		t.Fatal("Approve timed out — answer likely dropped")
	}

	// Checked only after Approve() has fully returned, so cancelOverlayWork
	// (called synchronously, before Approve() ever unlocks) has definitely
	// already run one way or the other — no ordering race with the assertion.
	if cancelled.Load() {
		t.Fatal("btw's underlying stream must not be cancelled by a concurrent approval prompt")
	}

	tui.mu.Lock()
	restored, ok := tui.activeOverlay.(*btwOverlay)
	tui.mu.Unlock()
	if !ok || restored != btw {
		t.Fatal("expected the original btw overlay to be restored after approval, not replaced or cleared")
	}
}

// TestApprove_MainOriginParksBehindOpenBTWInstead is the regression test for
// the /btw + bash coexistence bug: a main-turn (or subagent) approval firing
// while /btw's own panel is showing must NOT destroy it — it parks silently
// behind it (no cancel, no overlay swap, /btw keeps showing and running) and
// only surfaces its own prompt once the human closes /btw themselves.
func TestApprove_MainOriginParksBehindOpenBTWInstead(t *testing.T) {
	tui := newTestTUIHelper()
	btw := newBTWOverlay("unrelated leftover overlay")
	var cancelled atomic.Bool
	btw.setCancel(func() { cancelled.Store(true) })
	tui.mu.Lock()
	tui.activeOverlay = btw
	tui.currentBTW = btw // openBTW invariant — see TUI.currentBTW
	tui.mu.Unlock()

	result := make(chan bool, 1)
	go func() {
		allowed, _ := tui.Approve(context.Background(), "rm -rf x", "danger", "", agent.BashRiskHigh, agent.ApprovalOriginMain)
		result <- allowed
	}()

	// Give Approve() time to run past the park-or-show decision. approving
	// is set true immediately regardless (it just blocks background input);
	// what must NOT have happened yet is btw being cancelled or replaced.
	time.Sleep(50 * time.Millisecond)
	if cancelled.Load() {
		t.Fatal("/btw's underlying stream must not be cancelled by a parked main-origin approval")
	}
	tui.mu.Lock()
	_, stillBTW := tui.activeOverlay.(*btwOverlay)
	tui.mu.Unlock()
	if !stillBTW {
		t.Fatal("expected /btw's panel to still be the active overlay while parked")
	}

	// User closes /btw themselves (Esc while processing → cancelOverlayWork,
	// which signals closedCh — see cancelOverlayWork).
	tui.mu.Lock()
	tui.dismissOverlay()
	tui.mu.Unlock()

	deadline := time.Now().Add(500 * time.Millisecond)
	isApprovalOverlay := func() bool {
		tui.mu.Lock()
		defer tui.mu.Unlock()
		_, ok := tui.activeOverlay.(*approvalOverlay)
		return ok
	}
	for !isApprovalOverlay() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !isApprovalOverlay() {
		t.Fatal("Approve never surfaced its prompt after /btw closed")
	}
	if !cancelled.Load() {
		t.Fatal("expected /btw's stream cancelled once the user actually closes it")
	}

	tui.approvalAnswer <- approvalReply{Allowed: true}
	select {
	case <-result:
	case <-time.After(time.Second):
		t.Fatal("Approve timed out — answer likely dropped")
	}

	tui.mu.Lock()
	defer tui.mu.Unlock()
	if tui.activeOverlay != nil {
		t.Fatal("expected activeOverlay cleared (not restored) after a main-origin approval")
	}
}

// TestBTWNormalEscCloseAfterFinishUnparksLaterApprovals is the regression
// test for closeOverlayAfter's done-but-not-cancel branch (the ordinary way
// to dismiss /btw: read the finished answer, then press Esc) previously
// clearing only activeOverlay and leaving t.currentBTW pointing at the
// already-closed overlay forever — its closedCh never signaled, so every
// later non-btw approval parked on <-b.closedCh() and never showed its
// prompt at all. This is the single most common way to use /btw at all
// (open it, wait for the answer, close it), so the bug fired on nearly
// every real session that used the feature.
func TestBTWNormalEscCloseAfterFinishUnparksLaterApprovals(t *testing.T) {
	tui := newTestTUIHelper()
	btw := newBTWOverlay("what's in this file?")
	btw.finish(nil) // answer already streamed in — the normal pre-close state
	tui.mu.Lock()
	tui.activeOverlay = btw
	tui.currentBTW = btw // openBTW invariant — see TUI.currentBTW
	tui.mu.Unlock()

	// Normal close: Esc while not processing. feedKey returns
	// done=true, cancel=false (proc was false) — this is the path that
	// used to leak.
	tui.mu.Lock()
	handled := tui.handleKeyOverlay(Key{Kind: KeyEscape})
	overlayGone := tui.activeOverlay == nil
	tui.mu.Unlock()
	if !handled {
		t.Fatal("Esc was not handled by the btw overlay")
	}
	if !overlayGone {
		t.Fatal("expected activeOverlay cleared after Esc-close")
	}

	tui.mu.Lock()
	stillTracked := tui.currentBTW != nil
	tui.mu.Unlock()
	if stillTracked {
		t.Fatal("currentBTW still points at the closed overlay — later approvals will park forever")
	}

	// A later, unrelated main-origin approval must show its prompt
	// immediately — not park behind a phantom /btw session.
	result := make(chan bool, 1)
	go func() {
		allowed, _ := tui.Approve(context.Background(), "rm -rf x", "cleanup", "/tmp", agent.BashRiskHigh, agent.ApprovalOriginMain)
		result <- allowed
	}()

	deadline := time.Now().Add(500 * time.Millisecond)
	isApprovalOverlay := func() bool {
		tui.mu.Lock()
		defer tui.mu.Unlock()
		_, ok := tui.activeOverlay.(*approvalOverlay)
		return ok
	}
	for !isApprovalOverlay() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !isApprovalOverlay() {
		t.Fatal("approval prompt never surfaced — still stuck parked behind the (already closed) /btw overlay")
	}

	tui.approvalAnswer <- approvalReply{Allowed: true}
	select {
	case <-result:
	case <-time.After(time.Second):
		t.Fatal("Approve timed out — answer likely dropped")
	}
}

// TestApprove_BTWOwnApprovalNotBlockedByParkedMainApproval is the deadlock
// regression test: a multi-tool-call /btw side question routinely needs its
// own approval mid-stream. That must keep working even while a main-turn (or
// subagent) approval is already parked behind /btw's still-open panel —
// approvalMu is a single lock shared by every origin, so parking while
// holding it would let the parked call block /btw's own Approve() forever,
// which in turn is the only thing that could ever close /btw and unpark it.
func TestApprove_BTWOwnApprovalNotBlockedByParkedMainApproval(t *testing.T) {
	tui := newTestTUIHelper()
	btw := newBTWOverlay("side question")
	tui.mu.Lock()
	tui.activeOverlay = btw
	tui.currentBTW = btw // openBTW invariant — see TUI.currentBTW
	tui.mu.Unlock()

	mainResult := make(chan bool, 1)
	go func() {
		allowed, _ := tui.Approve(context.Background(), "rm -rf x", "danger", "", agent.BashRiskHigh, agent.ApprovalOriginMain)
		mainResult <- allowed
	}()

	// Give the main-origin call time to park (activeOverlay stays btw).
	time.Sleep(50 * time.Millisecond)
	tui.mu.Lock()
	_, stillBTW := tui.activeOverlay.(*btwOverlay)
	tui.mu.Unlock()
	if !stillBTW {
		t.Fatal("expected main-origin approval to have parked behind /btw")
	}

	// /btw's own side question now needs its own approval — this must not
	// deadlock against the parked main-origin call above.
	btwResult := make(chan bool, 1)
	go func() {
		allowed, _ := tui.Approve(context.Background(), "ls", "list", "", agent.BashRiskLow, agent.ApprovalOriginBTW)
		btwResult <- allowed
	}()

	isApprovalOverlay := func() bool {
		tui.mu.Lock()
		defer tui.mu.Unlock()
		_, ok := tui.activeOverlay.(*approvalOverlay)
		return ok
	}
	deadline := time.Now().Add(2 * time.Second)
	for !isApprovalOverlay() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !isApprovalOverlay() {
		t.Fatal("DEADLOCK: btw-origin Approve never got to show its own prompt while a main-origin approval was parked behind it")
	}
	// Answer btw's own approval.
	tui.approvalAnswer <- approvalReply{Allowed: true}
	select {
	case <-btwResult:
	case <-time.After(time.Second):
		t.Fatal("btw-origin Approve never returned after being answered")
	}

	// btw's own approval answered → its panel is restored, still open, main
	// is still parked behind it. Now the user closes /btw for real.
	tui.mu.Lock()
	_, stillBTWAfter := tui.activeOverlay.(*btwOverlay)
	tui.dismissOverlay()
	tui.mu.Unlock()
	if !stillBTWAfter {
		t.Fatal("expected /btw's panel restored (still open) after answering its own nested approval")
	}

	deadline = time.Now().Add(time.Second)
	for !isApprovalOverlay() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !isApprovalOverlay() {
		t.Fatal("parked main-origin approval never surfaced its prompt after /btw closed")
	}
	tui.approvalAnswer <- approvalReply{Allowed: true}
	select {
	case <-mainResult:
	case <-time.After(time.Second):
		t.Fatal("parked main-origin approval never completed after btw closed")
	}
}

// TestApprovalOriginLabel_ShownForBTWAndSubagentNotMain verifies the panel
// title carries an origin badge for btw/subagent but stays plain for the
// ordinary main-conversation case.
func TestApprovalOriginLabel_ShownForBTWAndSubagentNotMain(t *testing.T) {
	main := newApprovalOverlay("ls", "list", "", agent.ApprovalOriginMain)
	if got := approvalOriginLabel(main.origin); got != "" {
		t.Errorf("main origin label = %q, want empty", got)
	}

	btwOv := newApprovalOverlay("ls", "list", "", agent.ApprovalOriginBTW)
	lines := btwOv.renderInputPanel(8, 80)
	if !strings.Contains(stripANSI(lines[0]), "/btw") {
		t.Errorf("title = %q, want a /btw badge", stripANSI(lines[0]))
	}

	sub := newApprovalOverlay("ls", "list", "", agent.SubagentOrigin("scout"))
	lines = sub.renderInputPanel(8, 80)
	if !strings.Contains(stripANSI(lines[0]), "subagent scout") {
		t.Errorf("title = %q, want a subagent scout badge", stripANSI(lines[0]))
	}
}
