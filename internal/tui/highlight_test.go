package tui

import (
	"strings"
	"testing"
)

func TestSplitFenceSegments(t *testing.T) {
	segs := splitFenceSegments("hello\n```go\nfunc main(){}\n```\nworld")
	if len(segs) != 3 {
		t.Fatalf("segments = %d", len(segs))
	}
	if segs[0].text != "hello\n" || segs[1].code != true || segs[1].lang != "go" || segs[2].text != "world" {
		t.Fatalf("%+v", segs)
	}
}

// TestSplitFenceSegmentsIgnoresFenceInsideInlineCodeSpan is a regression test
// for a live user report: a message containing an inline single-backtick code
// span whose content happened to include a literal "```" run (e.g. a sentence
// explaining a grep command: "verified — `grep -c '```js'` → 0") corrupted
// the rendering of everything after it — the old substring scan treated the
// "```" inside that inline span as a real fence open, then swallowed the rest
// of the message (including an unrelated closing question) into one bogus
// code block whose "language" was several lines of prose, overflowing the
// code-block border past the terminal width. A real fence delimiter must be
// alone on its line; this one is buried mid-sentence.
func TestSplitFenceSegmentsIgnoresFenceInsideInlineCodeSpan(t *testing.T) {
	src := "Original:\n```js\nconst kv = 1;\n```\n" +
		"`docs/API.md`'s zero code blocks (verified — `grep -c '```js'` → 0), pure prose.\n\n" +
		"❓ Want me to patch these now, or leave as a follow-up?"
	segs := splitFenceSegments(src)

	var codeSegs, proseSegs int
	for _, s := range segs {
		if s.code {
			codeSegs++
			if s.lang != "js" {
				t.Errorf("code segment lang = %q, want %q (a mis-parsed multi-line lang means the bug reproduced)", s.lang, "js")
			}
		} else {
			proseSegs++
		}
	}
	if codeSegs != 1 {
		t.Fatalf("code segments = %d, want exactly 1 (the real ```js fence) — got %+v", codeSegs, segs)
	}
	joined := segs[len(segs)-1].text
	if !strings.Contains(joined, "Want me to patch") {
		t.Errorf("trailing question should render as prose, not get swallowed into a code block: %+v", segs)
	}
}

func TestSplitFenceUnclosed(t *testing.T) {
	segs := splitFenceSegments("```go\nfmt.Println")
	if len(segs) != 1 || !segs[0].code {
		t.Fatalf("%+v", segs)
	}
}

func TestHighlightGoKeywords(t *testing.T) {
	got := highlightLine("go", "func main() string {")
	if !stringsContains(got, "\x1b[1m") || !stringsContains(got, "func") {
		t.Fatalf("got %q", got)
	}
}

func TestHighlightUnknownLangPlain(t *testing.T) {
	got := stripANSI(highlightCode("nope", "plain text"))
	if got != "plain text" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderCodeBlockBorder(t *testing.T) {
	lines := renderCodeBlock("go", "go", "x := 1", 30, "")
	if len(lines) < 3 {
		t.Fatalf("lines = %d", len(lines))
	}
	if !strings.Contains(stripANSI(lines[0]), "╭") || !strings.Contains(stripANSI(lines[len(lines)-1]), "╰") {
		t.Fatalf("missing box: %v", lines)
	}
}

// TestBoxTopTruncatesOverlongLabel is defense-in-depth: even with the fence
// mis-parse above fixed, boxTop must never emit an unbounded line — the same
// "untruncated line overflows the terminal and corrupts the screen" bug shape
// already found and fixed once this session for the footer hint line.
func TestBoxTopTruncatesOverlongLabel(t *testing.T) {
	label := strings.Repeat("x", 200)
	got := boxTop(label, 40)
	if w := visibleWidth(stripANSI(got)); w > 40 {
		t.Fatalf("boxTop width = %d, want <= 40 (label: %d chars)", w, len(label))
	}
}

func TestLayoutRichMarkdownWithFence(t *testing.T) {
	raw := "text\n```go\nfunc f(){}\n```"
	lines := layoutRichMarkdown(raw, 40, fgGreen)
	if len(lines) < 4 {
		t.Fatalf("lines = %d", len(lines))
	}
	foundBox := false
	for _, ln := range lines {
		if strings.Contains(stripANSI(ln), "╭") {
			foundBox = true
		}
	}
	if !foundBox {
		t.Fatal("expected code box")
	}
}

func TestHighlightJSONBooleans(t *testing.T) {
	got := highlightLine("json", `{"ok": true}`)
	if !stringsContains(got, "true") {
		t.Fatalf("got %q", got)
	}
}
