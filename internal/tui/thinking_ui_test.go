package tui

import (
	"strings"
	"testing"
)

// =============================================================================
// UI-level tests for thinking blocks in the scrollback.
//
// These tests exercise the scrollback's data model + rendering — the same
// layer the TUI uses. They verify that thinking events create blocks, that
// blocks are expanded while streaming and collapsed after finalization, that
// Ctrl+T toggles them, and that the rendered output contains the right text
// and styling.
// =============================================================================

// appendThinking is a helper that appends a thinking event to the scrollback.
func appendThinking(s *scrollback, text string) {
	s.append(StyledLine{Style: styleThinking, Text: text})
}

// renderedText returns the visible rows as plain text (ANSI stripped), joined.
func renderedText(s *scrollback, height, width int) string {
	rows := s.visible(height, width)
	var parts []string
	for _, r := range rows {
		parts = append(parts, stripANSI(r.Text))
	}
	return strings.Join(parts, "\n")
}

// blockMeta returns the meta of block i (test helper).
func blockMeta(s *scrollback, i int) *BlockMeta {
	if i < 0 || i >= len(s.blocks) {
		return nil
	}
	return &s.blocks[i].meta
}

// =============================================================================
// Tests
// =============================================================================

// TestUIThinking_BlockCreatedFromEvents verifies that appending thinking
// events creates a single thinking block with the accumulated text.
func TestUIThinking_BlockCreatedFromEvents(t *testing.T) {
	s := newScrollback(1024)

	appendThinking(s, "The user said hello. ")
	appendThinking(s, "I should respond warmly.")

	if s.blockCount() != 1 {
		t.Fatalf("expected 1 block, got %d", s.blockCount())
	}
	raw := s.blockRaw(0)
	if !strings.Contains(raw, "The user said hello.") || !strings.Contains(raw, "I should respond warmly.") {
		t.Errorf("thinking text not accumulated: %q", raw)
	}
}

// TestUIThinking_ExpandedWhileStreaming verifies that a streaming thinking
// block is not collapsed — the full text should be visible in the rendered output.
func TestUIThinking_ExpandedWhileStreaming(t *testing.T) {
	s := newScrollback(1024)
	appendThinking(s, "Let me think about this problem carefully.")
	// While streaming, the block should be expanded.
	meta := blockMeta(s, 0)
	if meta == nil || meta.Collapsed {
		t.Fatal("expected streaming thinking block to be expanded (Collapsed=false)")
	}
	if !meta.Streaming {
		t.Fatal("expected Streaming=true while streaming")
	}

	// The rendered output should contain the thinking text.
	out := renderedText(s, 10, 80)
	if !strings.Contains(out, "Let me think") {
		t.Errorf("streaming thinking text not visible: %q", out)
	}
}

// TestUIThinking_CollapsedAfterFinalize verifies that finalizeThinking
// collapses the block and the rendered output shows the one-line header.
func TestUIThinking_CollapsedAfterFinalize(t *testing.T) {
	s := newScrollback(1024)
	appendThinking(s, "This is a detailed reasoning process about the problem.")
	s.finalizeThinking()

	meta := blockMeta(s, 0)
	if meta == nil || !meta.Collapsed {
		t.Fatal("expected Collapsed=true after finalizeThinking")
	}
	if meta.Streaming {
		t.Fatal("expected Streaming=false after finalizeThinking")
	}

	out := renderedText(s, 10, 80)
	// Collapsed header should mention "thinking" with a char count.
	if !strings.Contains(out, "thinking") {
		t.Errorf("collapsed header missing 'thinking': %q", out)
	}
	// The full reasoning text should NOT be visible when collapsed.
	if strings.Contains(out, "This is a detailed reasoning process") {
		t.Errorf("full text should not be visible when collapsed: %q", out)
	}
}

// TestUIThinking_ToggleExpands verifies that toggleLastThinking expands a
// collapsed thinking block, making the full text visible again.
func TestUIThinking_ToggleExpands(t *testing.T) {
	s := newScrollback(1024)
	appendThinking(s, "Deep reasoning about the answer.")
	s.finalizeThinking()

	// Collapsed → text hidden.
	outCollapsed := renderedText(s, 10, 80)
	if strings.Contains(outCollapsed, "Deep reasoning") {
		t.Errorf("text should be hidden when collapsed: %q", outCollapsed)
	}

	// Toggle → expanded → text visible.
	toggled, collapsed := s.toggleLastThinking()
	if !toggled {
		t.Fatal("toggle should return true (found a block)")
	}
	if collapsed {
		t.Error("expected collapsed=false after toggle (expanding)")
	}

	outExpanded := renderedText(s, 10, 80)
	if !strings.Contains(outExpanded, "Deep reasoning") {
		t.Errorf("text should be visible after toggle: %q", outExpanded)
	}
}

// TestUIThinking_ToggleCollapses verifies that toggling an expanded block
// collapses it again.
func TestUIThinking_ToggleCollapses(t *testing.T) {
	s := newScrollback(1024)
	appendThinking(s, "Some thoughts here.")
	s.finalizeThinking()

	// First toggle: expand.
	s.toggleLastThinking()
	meta := blockMeta(s, 0)
	if meta.Collapsed {
		t.Fatal("expected expanded after first toggle")
	}

	// Second toggle: collapse.
	toggled, collapsed := s.toggleLastThinking()
	if !toggled || !collapsed {
		t.Fatal("second toggle should return (true, true)")
	}

	out := renderedText(s, 10, 80)
	if strings.Contains(out, "Some thoughts here.") {
		t.Errorf("text should be hidden after second toggle: %q", out)
	}
}

// TestUIThinking_TextIsItalic verifies that expanded thinking text is
// rendered with dim+italic ANSI styling.
func TestUIThinking_TextIsItalic(t *testing.T) {
	s := newScrollback(1024)
	appendThinking(s, "reasoning text")
	s.finalizeThinking()
	s.toggleLastThinking() // expand

	rows := s.visible(10, 80)
	var foundStyled bool
	for _, r := range rows {
		raw := r.Text
		// dim = \x1b[2m, italic = \x1b[3m
		if strings.Contains(raw, "\x1b[2m") && strings.Contains(raw, "\x1b[3m") {
			if strings.Contains(stripANSI(raw), "reasoning text") {
				foundStyled = true
			}
		}
	}
	if !foundStyled {
		t.Error("expected thinking text to be rendered with dim+italic")
	}
}

// TestUIThinking_TextFinalizesThinking verifies that appending assistant text
// after thinking events finalizes (collapses) the thinking block, and the
// text block follows it.
func TestUIThinking_TextFinalizesThinking(t *testing.T) {
	s := newScrollback(1024)
	appendThinking(s, "my reasoning")
	s.finalizeThinking()
	s.append(StyledLine{Style: styleAssistant, Text: "my answer"})

	// After text, thinking should be finalized (collapsed).
	meta := blockMeta(s, 0)
	if meta == nil || !meta.Collapsed {
		t.Fatal("expected thinking block to be collapsed after text arrives")
	}

	// Two blocks: thinking + assistant.
	if s.blockCount() != 2 {
		t.Fatalf("expected 2 blocks, got %d", s.blockCount())
	}

	// Rendered output should show the collapsed thinking header, then the answer.
	out := renderedText(s, 10, 80)
	if !strings.Contains(out, "thinking") {
		t.Errorf("collapsed thinking header missing: %q", out)
	}
	if !strings.Contains(out, "my answer") {
		t.Errorf("assistant text missing: %q", out)
	}
}

// TestUIThinking_RedactedShown verifies that redacted thinking blocks are
// created and shown as collapsed.
func TestUIThinking_RedactedShown(t *testing.T) {
	s := newScrollback(1024)
	s.appendThinkingRedacted()

	if s.blockCount() != 1 {
		t.Fatalf("expected 1 block, got %d", s.blockCount())
	}
	meta := blockMeta(s, 0)
	if meta == nil || !meta.ThinkingRedacted {
		t.Error("expected ThinkingRedacted=true")
	}
	if !meta.Collapsed {
		t.Error("expected redacted thinking to be collapsed")
	}

	out := renderedText(s, 10, 80)
	if !strings.Contains(out, "redacted") {
		t.Errorf("expected 'redacted' in output: %q", out)
	}
}

// TestUIThinking_RedactedNotToggleable verifies that toggleLastThinking
// returns false for a redacted thinking block.
func TestUIThinking_RedactedNotToggleable(t *testing.T) {
	s := newScrollback(1024)
	s.appendThinkingRedacted()

	toggled, _ := s.toggleLastThinking()
	if toggled {
		t.Error("toggle should return false for redacted thinking")
	}
}

// TestUIThinking_MultipleThinkingBlocks verifies that separate turns create
// separate thinking blocks, and only the last one is toggled.
func TestUIThinking_MultipleThinkingBlocks(t *testing.T) {
	s := newScrollback(1024)

	// Turn 1: thinking + answer.
	appendThinking(s, "first reasoning")
	s.finalizeThinking()
	s.append(StyledLine{Style: styleAssistant, Text: "first answer"})

	// Turn 2: thinking + answer.
	appendThinking(s, "second reasoning")
	s.finalizeThinking()
	s.append(StyledLine{Style: styleAssistant, Text: "second answer"})

	if s.blockCount() != 4 {
		t.Fatalf("expected 4 blocks, got %d", s.blockCount())
	}

	// Toggle should affect only the LAST thinking block (block 2).
	toggled, _ := s.toggleLastThinking()
	if !toggled {
		t.Fatal("toggle should find the last thinking block")
	}

	// Block 2 should be expanded, block 0 should still be collapsed.
	if !blockMeta(s, 0).Collapsed {
		t.Error("first thinking block should still be collapsed")
	}
	if blockMeta(s, 2).Collapsed {
		t.Error("second thinking block should be expanded after toggle")
	}
}

// TestUIThinking_ToggleNoThinkingBlock verifies that toggleLastThinking
// returns false when there are no thinking blocks.
func TestUIThinking_ToggleNoThinkingBlock(t *testing.T) {
	s := newScrollback(1024)
	s.append(StyledLine{Style: styleAssistant, Text: "just text"})

	toggled, _ := s.toggleLastThinking()
	if toggled {
		t.Error("toggle should return false when no thinking block exists")
	}
}

// TestUIThinking_ThinkingBeforeText verifies the rendering order: thinking
// block comes before the assistant text in the visible output.
func TestUIThinking_ThinkingBeforeText(t *testing.T) {
	s := newScrollback(1024)
	appendThinking(s, "my reasoning")
	s.finalizeThinking()
	s.append(StyledLine{Style: styleAssistant, Text: "my answer"})

	rows := s.visible(10, 80)
	var thinkingIdx, textIdx int = -1, -1
	for i, r := range rows {
		plain := stripANSI(r.Text)
		if strings.Contains(plain, "thinking") && thinkingIdx == -1 {
			thinkingIdx = i
		}
		if strings.Contains(plain, "my answer") && textIdx == -1 {
			textIdx = i
		}
	}
	if thinkingIdx == -1 {
		t.Error("thinking header not found in rendered output")
	}
	if textIdx == -1 {
		t.Error("assistant text not found in rendered output")
	}
	if thinkingIdx >= textIdx {
		t.Errorf("thinking (row %d) should come before text (row %d)", thinkingIdx, textIdx)
	}
}

// TestUIThinking_ThinkingThenMoreThinking verifies that consecutive thinking
// events merge into a single block (not one block per event).
func TestUIThinking_ThinkingThenMoreThinking(t *testing.T) {
	s := newScrollback(1024)
	appendThinking(s, "part 1. ")
	appendThinking(s, "part 2. ")
	appendThinking(s, "part 3.")

	// All three events should merge into one thinking block.
	if s.blockCount() != 1 {
		t.Fatalf("expected 1 merged block, got %d", s.blockCount())
	}
	raw := s.blockRaw(0)
	if !strings.Contains(raw, "part 1.") || !strings.Contains(raw, "part 3.") {
		t.Errorf("merged text incomplete: %q", raw)
	}
}