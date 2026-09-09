package tui

import "testing"

func TestSpinnerCharCycles(t *testing.T) {
	if spinnerChar(0) != "⠋" {
		t.Fatalf("frame 0 = %q", spinnerChar(0))
	}
	if spinnerChar(len(spinnerFrames)) != spinnerChar(0) {
		t.Fatalf("frame should wrap")
	}
}

func TestNeedsSpinner(t *testing.T) {
	if needsSpinner(false, 0, false, false) {
		t.Fatal("idle should not need spinner")
	}
	if !needsSpinner(true, 0, false, false) {
		t.Fatal("thinking should need spinner")
	}
	if !needsSpinner(false, 1, false, false) {
		t.Fatal("active tools should need spinner")
	}
	if !needsSpinner(false, 0, true, false) {
		t.Fatal("compacting should need spinner")
	}
	if !needsSpinner(false, 0, false, true) {
		t.Fatal("a running background subagent (main turn otherwise idle) should need spinner")
	}
}

func TestCompactionSpinnerTickMarksHeaderDirty(t *testing.T) {
	tui := newTUI(nil, "session", nil)
	tui.rows = 24
	tui.cols = 80
	tui.compacting.Store(true)
	tui.dirty.consume()

	tui.markSpinnerTick()

	if snap := tui.dirty.consume(); !snap.status {
		t.Fatal("compaction spinner tick should mark header dirty")
	}
}

// TestIdleMainAgentStillAnimatesRunningSubagent is the regression test for
// the async-subagent gap: the main turn can go fully idle (no thinking, no
// active tools) while a background subagent job is still running, and its
// pinned widget's spinner/live timer must keep animating regardless — see
// needsSpinner's runningSubagent parameter and markSpinnerTick's doc
// comment.
func TestIdleMainAgentStillAnimatesRunningSubagent(t *testing.T) {
	tui := newTUI(nil, "session", nil)
	tui.rows = 24
	tui.cols = 80
	tui.scrollRows = 20
	tui.scroll.appendSubagentCard(1, "call-1", "scout", "look around", "anthropic/claude-opus-5")

	if needsSpinner(tui.status.Thinking, tui.activeTools, tui.compacting.Load(), tui.scroll.hasRunningSubagent()) == false {
		t.Fatal("needsSpinner should report true while a subagent is running, even with the main agent idle")
	}

	tui.dirty.consume()
	tui.markSpinnerTick()
	if snap := tui.dirty.consume(); len(snap.scroll) == 0 {
		t.Fatal("spinner tick should repaint the pinned subagent lines even while the main agent is idle")
	}
}
