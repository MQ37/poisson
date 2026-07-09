package tui

import (
	"strings"
	"testing"
)

func TestScrollbackMergesStreamingAssistantLines(t *testing.T) {
	s := newScrollback(1024)
	s.append(StyledLine{Style: styleAssistant, Text: "Hello, "})
	s.append(StyledLine{Style: styleAssistant, Text: "world!"})
	if s.blockCount() != 1 {
		t.Fatalf("expected 1 block, got %d", s.blockCount())
	}
	if s.blockRaw(0) != "Hello, world!" {
		t.Errorf("merged text = %q", s.blockRaw(0))
	}

	s.appendRaw(styleToolResult, "done")
	if s.blockCount() != 2 {
		t.Fatalf("expected 2 blocks after tool result, got %d", s.blockCount())
	}

	s.append(StyledLine{Style: styleAssistant, Text: "Next"})
	if s.blockCount() != 3 {
		t.Fatalf("expected 3 blocks, got %d", s.blockCount())
	}
}

func TestScrollbackVisibleWrapsLongLines(t *testing.T) {
	s := newScrollback(1024)
	s.append(StyledLine{Style: styleAssistant, Text: strings.Repeat("a", 120)})
	rows := s.visible(10, 40)
	if len(rows) != 3 {
		t.Fatalf("expected 3 wrapped rows, got %d", len(rows))
	}
	for i, r := range rows {
		plain := stripANSI(r.Text)
		if len(plain) > 40 {
			t.Errorf("row %d exceeds width: %d", i, len(plain))
		}
	}
}

func TestScrollbackVisibleNoPanicOnOverflow(t *testing.T) {
	s := newScrollback(10)
	for i := 0; i < 50; i++ {
		s.append(StyledLine{Style: styleAssistant, Text: "x"})
	}
	// Scroll way up past the trim point.
	s.scrollOffset = 100
	rows := s.visible(5, 20)
	// Should return safely (possibly empty) without panicking.
	if len(rows) > 5 {
		t.Errorf("expected at most 5 rows, got %d", len(rows))
	}
}

func TestScrollbackSplitsNewlines(t *testing.T) {
	s := newScrollback(1024)
	// Non-streaming, non-user multiline (e.g. a multi-line error/system
	// message) becomes separate rows.
	s.append(StyledLine{Style: styleError, Text: "AAA\nBBB\nCCC"})
	if s.blockCount() != 3 {
		t.Fatalf("expected 3 blocks, got %d", s.blockCount())
	}
	if s.blockRaw(0) != "AAA" || s.blockRaw(1) != "BBB" || s.blockRaw(2) != "CCC" {
		t.Errorf("blocks = %q/%q/%q", s.blockRaw(0), s.blockRaw(1), s.blockRaw(2))
	}
}

// TestScrollbackUserMultilineStaysOneBlock is a regression test: a user's
// multi-line message was being split into one blockUser per source line
// (same non-streaming path as any other kind), so userBlockIndices — and
// Shift+Left/Right conversation-turn navigation, which counts on one block
// per submitted message — treated each line of a single multi-line message
// as its own separate turn.
func TestScrollbackUserMultilineStaysOneBlock(t *testing.T) {
	s := newScrollback(1024)
	s.append(StyledLine{Style: styleUser, Text: "line one\nline two\nline three"})
	if s.blockCount() != 1 {
		t.Fatalf("expected 1 block for one multi-line message, got %d", s.blockCount())
	}
	if got := s.blockRaw(0); got != "line one\nline two\nline three" {
		t.Errorf("block raw = %q, want the full multi-line text preserved", got)
	}
	if idxs := s.userBlockIndices(); len(idxs) != 1 {
		t.Errorf("userBlockIndices = %v, want exactly 1 turn for 1 submitted message", idxs)
	}
}

func TestScrollbackStreamingNewlines(t *testing.T) {
	s := newScrollback(1024)
	// Streamed assistant text arriving in chunks that span newlines stays one block.
	s.append(StyledLine{Style: styleAssistant, Text: "Hel"})
	s.append(StyledLine{Style: styleAssistant, Text: "lo\nWor"})
	s.append(StyledLine{Style: styleAssistant, Text: "ld"})
	if s.blockCount() != 1 {
		t.Fatalf("expected 1 block, got %d", s.blockCount())
	}
	if s.blockRaw(0) != "Hello\nWorld" {
		t.Errorf("block = %q", s.blockRaw(0))
	}
}

func TestScrollbackStreamingPreservesCodeFence(t *testing.T) {
	s := newScrollback(1024)
	s.append(StyledLine{Style: styleAssistant, Text: "text"})
	s.append(StyledLine{Style: styleAssistant, Text: "\n```go\nmain()\n```"})
	if s.blockCount() != 1 {
		t.Fatalf("expected 1 block, got %d", s.blockCount())
	}
	lines := layoutRichMarkdown(s.blockRaw(0), 40, "")
	foundBox := false
	for _, ln := range lines {
		if strings.Contains(stripANSI(ln), "╭") {
			foundBox = true
		}
	}
	if !foundBox {
		t.Fatal("expected code fence box in layout")
	}
}

func TestScrollbackStreamViewportDirtyPinned(t *testing.T) {
	s := newScrollback(1024)
	s.append(StyledLine{Style: styleAssistant, Text: "hello"})
	rows := s.streamViewportDirty(10, 40)
	if len(rows) != 1 || rows[0] != 0 {
		t.Fatalf("first chunk dirty rows = %v, want [0]", rows)
	}
	s.append(StyledLine{Style: styleAssistant, Text: " world"})
	rows = s.streamViewportDirty(10, 40)
	if len(rows) != 1 || rows[0] != 0 {
		t.Fatalf("merge dirty rows = %v, want [0]", rows)
	}
	s.scrollOffset = 5
	if got := s.streamViewportDirty(10, 40); got != nil {
		t.Fatalf("scrolled up should not dirty viewport, got %v", got)
	}
}

func TestScrollbackStreamViewportDirtyOverflowRepaintsFullViewport(t *testing.T) {
	s := newScrollback(1024)
	// Separate one-line blocks so total wrapped rows exceed the viewport.
	for i := 0; i < 20; i++ {
		s.appendRaw(styleSystem, "filler row "+strings.Repeat("x", 8)+itoa(i))
	}
	viewH := 10
	width := 40
	wrapped, _ := s.layoutAll(width)
	if len(wrapped) <= viewH {
		t.Fatalf("test setup: wrapped %d rows, need > %d", len(wrapped), viewH)
	}
	rows := s.streamViewportDirty(viewH, width)
	if len(rows) != viewH {
		t.Fatalf("overflow viewport dirty = %d rows, want %d (%v)", len(rows), viewH, rows)
	}
	for i := 0; i < viewH; i++ {
		if rows[i] != i {
			t.Fatalf("row %d = %d, want %d", i, rows[i], i)
		}
	}
	s.append(StyledLine{Style: styleAssistant, Text: " streaming tail"})
	rows = s.streamViewportDirty(viewH, width)
	if len(rows) != viewH {
		t.Fatalf("after tail stream dirty = %d rows, want %d", len(rows), viewH)
	}
}

func TestScrollbackSanitizesControls(t *testing.T) {
	s := newScrollback(1024)
	s.appendRaw(styleSystem, "a\tb\x1bc\x7fd")
	if s.blockCount() != 1 {
		t.Fatalf("expected 1 block, got %d", s.blockCount())
	}
	if got := s.blockRaw(0); got != "a    bd" {
		t.Fatalf("sanitized block = %q, want %q", got, "a    bd")
	}
}
