package tui

import (
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
	tui.mu.Unlock()

	result := make(chan bool, 1)
	go func() {
		allowed, _ := tui.Approve("rm -rf /tmp/x", "cleanup", "/tmp", agent.BashRiskHigh, agent.ApprovalOriginBTW)
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

// TestApprove_MainOriginStillCancelsPriorOverlay verifies the pre-existing
// behavior (any non-btw origin) is unchanged: an approval prompt still
// replaces + cancels whatever overlay work was active, and does not try to
// restore it afterward.
func TestApprove_MainOriginStillCancelsPriorOverlay(t *testing.T) {
	tui := newTestTUIHelper()
	btw := newBTWOverlay("unrelated leftover overlay")
	var cancelled atomic.Bool
	btw.setCancel(func() { cancelled.Store(true) })
	tui.mu.Lock()
	tui.activeOverlay = btw
	tui.mu.Unlock()

	result := make(chan bool, 1)
	go func() {
		allowed, _ := tui.Approve("rm -rf x", "danger", "", agent.BashRiskHigh, agent.ApprovalOriginMain)
		result <- allowed
	}()

	// Approve() drains any stale answer left in the channel before it starts
	// really waiting — sending too early would just be discarded as "stale".
	// Wait until it's past that point (approving == true) first.
	deadline := time.Now().Add(500 * time.Millisecond)
	for !tui.approving.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !tui.approving.Load() {
		t.Fatal("Approve never entered approving state")
	}

	tui.approvalAnswer <- approvalReply{Allowed: true}
	select {
	case <-result:
	case <-time.After(time.Second):
		t.Fatal("Approve timed out — answer likely dropped")
	}

	// Checked only after Approve() has fully returned — see the sibling
	// btw-origin test's comment for why this ordering avoids a data race.
	if !cancelled.Load() {
		t.Fatal("expected the stale overlay's work to be cancelled for a main-origin approval")
	}

	tui.mu.Lock()
	defer tui.mu.Unlock()
	if tui.activeOverlay != nil {
		t.Fatal("expected activeOverlay cleared (not restored) after a main-origin approval")
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
