package tui

import (
	"strings"
	"testing"
)

func TestUpdateSubagentTurnsLiveWhileRunning(t *testing.T) {
	s := newScrollback(1024)
	s.appendSubagentCard(1, "call-1", "explore", "check the flow", "glm-5.2:cloud")

	s.updateSubagentTurns("call-1", 3)

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
	s.updateSubagentTurns("call-1", 5)
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
	s.updateSubagentTurns("call-1", 1)

	rows := layoutSubagentCard(&s.blocks[0], 80)
	plain := stripANSI(rows[0].Text)
	if !strings.Contains(plain, "1 turn") || strings.Contains(plain, "1 turns") {
		t.Errorf("expected singular '1 turn', got %q", plain)
	}
}

func TestUpdateSubagentTurnsNoMatchIsNoop(t *testing.T) {
	s := newScrollback(1024)
	s.appendSubagentCard(1, "call-1", "explore", "task", "model")
	s.updateSubagentTurns("call-does-not-exist", 9)

	if s.blocks[0].meta.SubagentTurns != 0 {
		t.Errorf("expected no change for a non-matching call id, got %d", s.blocks[0].meta.SubagentTurns)
	}
}
