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