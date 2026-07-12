package tui

import (
	"strings"
	"testing"
)

func TestUpdateSubagentTurnsLiveWhileRunning(t *testing.T) {
	s := newScrollback(1024)
	s.appendSubagentCard(1, "call-1", "explore", "check the flow", "glm-5.2:cloud")

	s.updateSubagentProgress("call-1", 3, 0, 0, "")

	if s.blocks[0].meta.SubagentTurns != 3 {
		t.Fatalf("SubagentTurns = %d, want 3", s.blocks[0].meta.SubagentTurns)
	}
	rows := layoutSubagentCard(&s.blocks[0], 80)
	plain := stripANSI(rows[0].Text)
	if !strings.Contains(plain, "3 turns") {
		t.Errorf("card should show turn count while running, got %q", plain)
	}
}

func TestUpdateSubagentTurnsSurvivesCompletion(t *testing.T) {
	s := newScrollback(1024)
	s.appendSubagentCard(1, "call-1", "explore", "check the flow", "glm-5.2:cloud")

	// Final progress update (Execute reports it right before returning,
	// strictly before the outer tool_result flips the card to done).
	s.updateSubagentProgress("call-1", 5, 0, 0, "")
	s.completeSubagentCard("call-1", "", 1200)

	rows := layoutSubagentCard(&s.blocks[0], 80)
	plain := stripANSI(rows[0].Text)
	if !strings.Contains(plain, "5 turns") {
		t.Errorf("completed card should keep showing the final turn count, got %q", plain)
	}
	if !strings.Contains(plain, "✓") {
		t.Errorf("expected done glyph, got %q", plain)
	}
}

func TestUpdateSubagentTurnsSingularUnit(t *testing.T) {
	s := newScrollback(1024)
	s.appendSubagentCard(1, "call-1", "explore", "task", "model")
	s.updateSubagentProgress("call-1", 1, 0, 0, "")

	rows := layoutSubagentCard(&s.blocks[0], 80)
	plain := stripANSI(rows[0].Text)
	if !strings.Contains(plain, "1 turn") || strings.Contains(plain, "1 turns") {
		t.Errorf("expected singular '1 turn', got %q", plain)
	}
}

// TestUpdateSubagentProgressShowsContextUsage verifies the card renders the
// child's own context usage as "used / window", matching the main header's
// format (formatNum), and that it survives completion like the turn count
// does.
func TestUpdateSubagentProgressShowsContextUsage(t *testing.T) {
	s := newScrollback(1024)
	s.appendSubagentCard(1, "call-1", "explore", "check the flow", "glm-5.2:cloud")

	s.updateSubagentProgress("call-1", 4, 12345, 200000, "")

	rows := layoutSubagentCard(&s.blocks[0], 120)
	plain := stripANSI(rows[0].Text)
	want := formatNum(12345) + " / " + formatNum(200000)
	if !strings.Contains(plain, want) {
		t.Errorf("card missing context usage %q, got %q", want, plain)
	}

	s.completeSubagentCard("call-1", "", 800)
	rows = layoutSubagentCard(&s.blocks[0], 120)
	plain = stripANSI(rows[0].Text)
	if !strings.Contains(plain, want) {
		t.Errorf("completed card lost context usage, got %q", plain)
	}
}

// TestUpdateSubagentProgressHidesContextUsageUntilReported verifies a card
// with no progress yet (ContextWindow == 0) doesn't render a bogus "0 / 0".
func TestUpdateSubagentProgressHidesContextUsageUntilReported(t *testing.T) {
	s := newScrollback(1024)
	s.appendSubagentCard(1, "call-1", "explore", "task", "model")

	rows := layoutSubagentCard(&s.blocks[0], 120)
	plain := stripANSI(rows[0].Text)
	if strings.Contains(plain, "/") {
		t.Errorf("card should not show context usage before any progress report, got %q", plain)
	}
}

func TestUpdateSubagentTurnsNoMatchIsNoop(t *testing.T) {
	s := newScrollback(1024)
	s.appendSubagentCard(1, "call-1", "explore", "task", "model")
	s.updateSubagentProgress("call-does-not-exist", 9, 0, 0, "")

	if s.blocks[0].meta.SubagentTurns != 0 {
		t.Errorf("expected no change for a non-matching call id, got %d", s.blocks[0].meta.SubagentTurns)
	}
}
