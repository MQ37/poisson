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
	lines := renderCodeBlock("go", "x := 1", 30, "")
	if len(lines) < 3 {
		t.Fatalf("lines = %d", len(lines))
	}
	if !strings.Contains(stripANSI(lines[0]), "╭") || !strings.Contains(stripANSI(lines[len(lines)-1]), "╰") {
		t.Fatalf("missing box: %v", lines)
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
