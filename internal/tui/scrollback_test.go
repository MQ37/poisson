package tui

import (
	"strings"
	"testing"
)

func TestScrollbackMergesStreamingAssistantLines(t *testing.T) {
	s := newScrollback(1024)
	s.append(StyledLine{Style: styleAssistant, Text: "Hello, "})
	s.append(StyledLine{Style: styleAssistant, Text: "world!"})
	if len(s.lines) != 1 {
		t.Fatalf("expected 1 logical line, got %d", len(s.lines))
	}
	if s.lines[0].Text != "Hello, world!" {
		t.Errorf("merged text = %q", s.lines[0].Text)
	}

	// A tool result breaks the stream and starts a new line.
	s.appendRaw(styleToolResult, "done")
	if len(s.lines) != 2 {
		t.Fatalf("expected 2 logical lines after tool result, got %d", len(s.lines))
	}

	// New assistant text after tool result starts a new line.
	s.append(StyledLine{Style: styleAssistant, Text: "Next"})
	if len(s.lines) != 3 {
		t.Fatalf("expected 3 logical lines, got %d", len(s.lines))
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
	s.scrollTop = 100
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
	if len(s.lines) != 3 {
		t.Fatalf("expected 3 logical lines, got %d: %#v", len(s.lines), s.lines)
	}
	if s.lines[0].Text != "AAA" || s.lines[1].Text != "BBB" || s.lines[2].Text != "CCC" {
		t.Errorf("lines = %q/%q/%q", s.lines[0].Text, s.lines[1].Text, s.lines[2].Text)
	}
}

func TestScrollbackStreamingNewlines(t *testing.T) {
	s := newScrollback(1024)
	// Streamed assistant text arriving in chunks that span newlines.
	s.append(StyledLine{Style: styleAssistant, Text: "Hel"})
	s.append(StyledLine{Style: styleAssistant, Text: "lo\nWor"})
	s.append(StyledLine{Style: styleAssistant, Text: "ld"})
	if len(s.lines) != 2 {
		t.Fatalf("expected 2 logical lines, got %d: %#v", len(s.lines), s.lines)
	}
	if s.lines[0].Text != "Hello" || s.lines[1].Text != "World" {
		t.Errorf("lines = %q / %q", s.lines[0].Text, s.lines[1].Text)
	}
}

func TestScrollbackNoEmbeddedNewline(t *testing.T) {
	s := newScrollback(1024)
	s.append(StyledLine{Style: styleAssistant, Text: "a\nb\nc"})
	for i, ln := range s.lines {
		if strings.Contains(ln.Text, "\n") || strings.Contains(ln.Text, "\r") {
			t.Errorf("line %d still contains a newline: %q", i, ln.Text)
		}
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
	s.scrollTop = 5
	if got := s.streamViewportDirty(10, 40); got != nil {
		t.Fatalf("scrolled up should not dirty viewport, got %v", got)
	}
}

func TestScrollbackSanitizesControls(t *testing.T) {
	s := newScrollback(1024)
	s.appendRaw(styleSystem, "a\tb\x1bc\x7fd")
	if len(s.lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(s.lines))
	}
	if got := s.lines[0].Text; got != "a    bd" {
		t.Fatalf("sanitized line = %q, want %q", got, "a    bd")
	}
}
