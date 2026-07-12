package tui

import "testing"

func TestRenderInlineBold(t *testing.T) {
	got := stripANSI(renderInline("**hello** world"))
	if got != "hello world" {
		t.Fatalf("got %q", got)
	}
	full := renderInline("**hello**")
	if !stringsContains(full, "\x1b[1m") {
		t.Fatalf("missing bold: %q", full)
	}
}

func TestRenderInlineItalic(t *testing.T) {
	full := renderInline("*em*")
	if !stringsContains(full, "\x1b[3m") {
		t.Fatalf("missing italic: %q", full)
	}
}

func TestRenderInlineCode(t *testing.T) {
	full := renderInline("use `foo` here")
	if !stringsContains(full, fgYellow) {
		t.Fatalf("missing code color: %q", full)
	}
}

func TestRenderInlineStrike(t *testing.T) {
	full := renderInline("~~gone~~")
	if stripANSI(full) != "gone" {
		t.Fatalf("got %q", full)
	}
}

func TestRenderInlineLink(t *testing.T) {
	full := renderInline("[docs](https://x.test)")
	if !stringsContains(full, "docs") || !stringsContains(full, "https://x.test") {
		t.Fatalf("got %q", full)
	}
}

func TestStyleMarkdownHeader(t *testing.T) {
	got := styleMarkdownLine("## Title")
	if !stringsContains(got, "Title") || !stringsContains(got, "\x1b[1m") {
		t.Fatalf("got %q", got)
	}
}

func TestStyleMarkdownBullet(t *testing.T) {
	got := styleMarkdownLine("- item")
	if !stringsContains(got, "•") {
		t.Fatalf("got %q", got)
	}
}

func TestRenderMarkdownWraps(t *testing.T) {
	lines := renderMarkdown("**abcdefghij**", 5, "")
	if len(lines) < 2 {
		t.Fatalf("expected wrap, got %d lines", len(lines))
	}
}

func TestRenderMarkdownResetsANSI(t *testing.T) {
	for _, tc := range []string{
		"**bold**",
		"*italic*",
		"`code`",
		"## H",
		"- x",
		"[a](http://b)",
	} {
		lines := renderMarkdown(tc, 80, fgGreen)
		for _, ln := range lines {
			if stringsHasUnclosedANSI(ln) {
				t.Fatalf("unclosed ANSI for %q: %q", tc, ln)
			}
		}
	}
}

func TestBlockAssistantUsesMarkdown(t *testing.T) {
	b := Block{id: 1, kind: blockAssistant, raw: "**Hi**"}
	rows := b.layoutPlain(40)
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	if !stringsContains(rows[0].Text, "\x1b[1m") {
		t.Fatalf("expected bold in %q", rows[0].Text)
	}
}

func stringsContains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOfString(s, sub) >= 0)
}

func indexOfString(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func stringsHasUnclosedANSI(s string) bool {
	// After strip, no ESC should remain; with reset appended per chunk this holds.
	return stringsContains(s, "\x1b[") && !stringsContains(s, reset)
}
