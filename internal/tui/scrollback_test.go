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

func TestStatusRenderUsesCRLF(t *testing.T) {
	s := StatusSnapshot{
		SessionID:     "s-12345678",
		Cwd:           "/tmp",
		Model:         "ollama/glm-5.2:cloud",
		ContextPct:    12.5,
		ContextTokens: 1234,
		OutputTokens:  56,
		Cost:          0.0012,
		ShowTokens:    true,
		ShowCost:      true,
	}
	out := s.Render(80)
	if !strings.Contains(out, "\r\n") {
		t.Errorf("status render should use CRLF between rows, got %q", out)
	}
	if strings.Count(out, "12.5%") != 1 {
		t.Errorf("expected exactly one context %% occurrence, got %q", out)
	}
}

func TestStatusRenderTruncatesTopRow(t *testing.T) {
	s := StatusSnapshot{
		SessionID: "s-12345678",
		Cwd:       "/a/very/long/path/that/would/overflow/the/status/bar",
		Model:     "ollama/" + strings.Repeat("x", 120),
	}
	out := s.Render(40)
	rows := strings.Split(out, "\r\n")
	if len(rows) != 2 {
		t.Fatalf("rows = %d", len(rows))
	}
	if visibleWidth(rows[0]) > 40 {
		t.Fatalf("top row width = %d, want <= 40: %q", visibleWidth(rows[0]), rows[0])
	}
}

func TestScrollbackSplitsNewlines(t *testing.T) {
	s := newScrollback(1024)
	// Non-streaming multiline (user echo) must become separate rows.
	s.append(StyledLine{Style: styleUser, Text: "AAA\nBBB\nCCC"})
	if s.blockCount() != 3 {
		t.Fatalf("expected 3 blocks, got %d", s.blockCount())
	}
	if s.blockRaw(0) != "AAA" || s.blockRaw(1) != "BBB" || s.blockRaw(2) != "CCC" {
		t.Errorf("blocks = %q/%q/%q", s.blockRaw(0), s.blockRaw(1), s.blockRaw(2))
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
