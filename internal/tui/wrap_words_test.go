package tui

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestWrapWordsProse(t *testing.T) {
	text := "hello world foo bar"
	lines := wrapWords(text, 11)
	if len(lines) != 2 {
		t.Fatalf("lines = %v", lines)
	}
	if lines[0] != "hello world" || lines[1] != "foo bar" {
		t.Fatalf("got %v", lines)
	}
}

// TestWrapLineHardWrapsOnNewline is a regression test: wrapLine (unlike
// wrapWords, which it wraps) must treat an embedded \n as a hard line break,
// not a regular character — this renderer positions the cursor per row and
// writes raw bytes, so a literal \n left inside one wrapped chunk moves the
// cursor instead of soft-wrapping, corrupting the display.
func TestWrapLineHardWrapsOnNewline(t *testing.T) {
	got := wrapLine("line one\nline two\nline three", 20)
	want := []string{"line one", "line two", "line three"}
	if len(got) != len(want) {
		t.Fatalf("wrapLine = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestWrapLineHardWrapsOnNewlineThenSoftWrapsEachParagraph(t *testing.T) {
	got := wrapLine("a long first paragraph that needs wrapping\nshort", 15)
	for _, ln := range got {
		if strings.Contains(ln, "\n") {
			t.Fatalf("wrapped chunk still contains a raw newline: %q in %q", ln, got)
		}
	}
	if got[len(got)-1] != "short" {
		t.Errorf("last chunk = %q, want %q", got[len(got)-1], "short")
	}
}

func TestWrapWordsBreaksAtSpace(t *testing.T) {
	text := "hello world!"
	lines := wrapWords(text, 8)
	if len(lines) != 2 || lines[0] != "hello" || lines[1] != "world!" {
		t.Fatalf("got %v", lines)
	}
}

func TestWrapWordsLongURL(t *testing.T) {
	url := "https://example.com/very/long/path/segment"
	lines := wrapWords(url, 20)
	if len(lines) < 2 {
		t.Fatalf("expected hard wrap, got %v", lines)
	}
	for _, ln := range lines {
		if utf8.RuneCountInString(ln) > 20 {
			t.Fatalf("line too wide: %q", ln)
		}
	}
	joined := strings.Join(lines, "")
	if joined != url {
		t.Fatalf("content changed: %q", joined)
	}
}

func TestWrapANSIWordBreak(t *testing.T) {
	src := bold + "hello world" + reset
	lines := wrapANSI(src, 8)
	if len(lines) != 2 {
		t.Fatalf("lines = %d %v", len(lines), lines)
	}
	if stripANSI(lines[0]) != "hello" || stripANSI(lines[1]) != "world" {
		t.Fatalf("got %v", lines)
	}
}

func TestWrapANSILongToken(t *testing.T) {
	src := renderInline("see https://example.com/longpath here")
	lines := wrapANSI(src, 24)
	if len(lines) < 2 {
		t.Fatalf("expected wrap, got %v", lines)
	}
	combined := stripANSI(strings.Join(lines, ""))
	if !strings.Contains(combined, "https://example.com/longpath") {
		t.Fatalf("lost url: %q", combined)
	}
}
