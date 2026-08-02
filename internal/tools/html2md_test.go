package tools

import (
	"strings"
	"testing"
)

func TestHTMLToMarkdown_Headings(t *testing.T) {
	got := htmlToMarkdown("<h1>Title</h1><h2>Sub</h2><p>Body text.</p>")
	want := "# Title\n\n## Sub\n\nBody text.\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestHTMLToMarkdown_BoldItalicCode(t *testing.T) {
	got := htmlToMarkdown("<p>This is <strong>bold</strong>, <em>italic</em>, and <code>inline code</code>.</p>")
	want := "This is **bold**, *italic*, and `inline code`.\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestHTMLToMarkdown_OrphanClosingTagDoesNotPanic reproduces a real crash:
// a stray closing tag with no matching open (single unmatched "</a>", "hi"
// forum/chat-dump HTML, etc.) used to underflow the internal builder
// stack's pop() and panic on the very next cur() call — inside
// FetchTool.fetchDirect, that turned "malformed HTML degrades gracefully"
// (this function's own doc comment) into an actual crash of the fetch
// call. Covers every close-case that calls pop() (a/strong/b/em/i/code/
// blockquote/tr/td/th), not just <a>.
func TestHTMLToMarkdown_OrphanClosingTagDoesNotPanic(t *testing.T) {
	cases := []string{
		"<html><body>hi</a>bye</body></html>",
		"<p>hi</strong>bye</p>",
		"<p>hi</b>bye</p>",
		"<p>hi</em>bye</p>",
		"<p>hi</i>bye</p>",
		"<p>hi</code>bye</p>",
		"<blockquote></blockquote></blockquote>",
		"<table><tr>a</tr></tr></table>",
		"<table><tr><td>a</td></td></tr></table>",
		"<table><tr><th>a</th></th></tr></table>",
	}
	for _, in := range cases {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("htmlToMarkdown(%q) panicked: %v", in, r)
				}
			}()
			htmlToMarkdown(in)
		}()
	}
}

func TestHTMLToMarkdown_NestedInline(t *testing.T) {
	got := htmlToMarkdown("<p><a href=\"https://example.com\"><strong>Click here</strong></a></p>")
	want := "[**Click here**](https://example.com)\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestHTMLToMarkdown_Image(t *testing.T) {
	got := htmlToMarkdown(`<p><img src="pic.png" alt="A picture"></p>`)
	want := "![A picture](pic.png)\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestHTMLToMarkdown_UnorderedList(t *testing.T) {
	got := htmlToMarkdown("<ul><li>one</li><li>two</li></ul>")
	want := "- one\n- two\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestHTMLToMarkdown_OrderedList(t *testing.T) {
	got := htmlToMarkdown("<ol><li>first</li><li>second</li></ol>")
	want := "1. first\n2. second\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestHTMLToMarkdown_Blockquote(t *testing.T) {
	got := htmlToMarkdown("<blockquote>a wise quote</blockquote>")
	want := "> a wise quote\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestHTMLToMarkdown_PreservesPreWhitespace(t *testing.T) {
	got := htmlToMarkdown("<pre>func main() {\n    fmt.Println(\"hi\")\n}</pre>")
	want := "```\nfunc main() {\n    fmt.Println(\"hi\")\n}\n```\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestHTMLToMarkdown_HorizontalRule(t *testing.T) {
	got := htmlToMarkdown("<p>above</p><hr><p>below</p>")
	want := "above\n\n---\n\nbelow\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestHTMLToMarkdown_Table(t *testing.T) {
	got := htmlToMarkdown("<table><tr><th>A</th><th>B</th></tr><tr><td>1</td><td>2</td></tr></table>")
	want := "| A | B |\n| 1 | 2 |\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestHTMLToMarkdown_StripsScriptAndStyle is the whole reason script/style
// get special tokenizer handling: their bodies routinely contain raw '<'/'>'
// (e.g. "if (a < b)") that would otherwise desync a generic tag scanner.
func TestHTMLToMarkdown_StripsScriptAndStyle(t *testing.T) {
	got := htmlToMarkdown(`<script>if (a < b) { doStuff("<broken>"); }</script>` +
		`<style>.x { color: red; }</style><p>real content</p>`)
	want := "real content\n"
	if got != want {
		t.Fatalf("got %q, want %q — script/style must not leak into output", got, want)
	}
}

func TestHTMLToMarkdown_StripsHeadAndNoscript(t *testing.T) {
	got := htmlToMarkdown("<head><title>Page Title</title><meta charset=\"utf-8\"></head>" +
		"<body><noscript>enable JS</noscript><p>visible</p></body>")
	if strings.Contains(got, "Page Title") || strings.Contains(got, "enable JS") {
		t.Fatalf("head/noscript content leaked into output: %q", got)
	}
	if !strings.Contains(got, "visible") {
		t.Fatalf("expected visible body content, got %q", got)
	}
}

func TestHTMLToMarkdown_EntityDecoding(t *testing.T) {
	got := htmlToMarkdown("<p>Tom &amp; Jerry &mdash; caf&#233; &#x2019;s</p>")
	want := "Tom & Jerry \u2014 caf\u00e9 \u2019s\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestHTMLToMarkdown_MalformedHTMLDoesNotPanic feeds deliberately broken
// markup (unclosed tags, a stray '<' with no matching '>', mismatched
// closes) through the converter — it must degrade gracefully, never panic
// or hang.
func TestHTMLToMarkdown_MalformedHTMLDoesNotPanic(t *testing.T) {
	inputs := []string{
		"<p>unclosed paragraph",
		"<div><span>nested unclosed",
		"text with a stray < in it",
		"</p>leading close tag with no open",
		"<a href=\"x\">link with no close",
		"<strong><em>deeply <a href=\"y\">nested unclosed",
		"<",
		"<!-- unterminated comment",
		"",
	}
	for _, in := range inputs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("htmlToMarkdown(%q) panicked: %v", in, r)
				}
			}()
			htmlToMarkdown(in)
		}()
	}
}

func TestDecodeEntities(t *testing.T) {
	cases := map[string]string{
		"a &amp; b":      "a & b",
		"&#65;&#66;":     "AB",
		"&#x41;&#x42;":   "AB",
		"no entities":    "no entities",
		"&unknown;stays": "&unknown;stays",
	}
	for in, want := range cases {
		if got := decodeEntities(in); got != want {
			t.Errorf("decodeEntities(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTokenizeHTML_AttrsWithGTInsideQuotes(t *testing.T) {
	toks := tokenizeHTML(`<a title="a > b" href="x">text</a>`)
	if len(toks) != 3 {
		t.Fatalf("expected 3 tokens (open, text, close), got %d: %+v", len(toks), toks)
	}
	if toks[0].kind != "open" || toks[0].name != "a" || toks[0].attrs["href"] != "x" {
		t.Fatalf("unexpected open tag token: %+v", toks[0])
	}
}
