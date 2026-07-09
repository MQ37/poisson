package tui

import (
	"strings"
	"testing"
)

// TestSanitizeControlsPreservesNewlines is a regression test: sanitizeControls
// dropped every \n (and \r) in its slow path (any string containing a tab or
// other control char), not just the actual control characters it was meant
// to strip. \n is 0x0A, which satisfies "r < 0x20" same as the control chars
// that really should be dropped (NUL, ESC, etc.) — silently collapsing
// multi-paragraph markdown (headers, bullets, code fences) into one flat
// line, but ONLY when the text also contained something like a tab
// (extremely common: models often indent code blocks with \t). The fast path
// (no control chars at all) masked this for plain prose, and for live
// streaming it only bit chunks that happened to contain both a tab and a
// newline — but hydrate replays a whole message in one shot, so one tab
// anywhere in a long reply wiped out every paragraph break in it, making
// resumed sessions render completely differently from how they looked live.
func TestSanitizeControlsPreservesNewlines(t *testing.T) {
	in := "line1\n\tindented\nline3"
	got := sanitizeControls(in)
	want := "line1\n    indented\nline3"
	if got != want {
		t.Errorf("sanitizeControls(%q) = %q, want %q", in, got, want)
	}
}

func TestSanitizeControlsStripsOtherControlChars(t *testing.T) {
	in := "a\x00b\x1bc\x7fd"
	got := sanitizeControls(in)
	want := "abcd"
	if got != want {
		t.Errorf("sanitizeControls(%q) = %q, want %q", in, got, want)
	}
}

func TestSanitizeControlsFastPathUnchanged(t *testing.T) {
	in := "plain text\nwith newlines\nno control chars"
	if got := sanitizeControls(in); got != in {
		t.Errorf("sanitizeControls(%q) = %q, want unchanged", in, got)
	}
}

// TestHydrateMatchesLiveRenderingWithTabsInText is a regression test for the
// user-visible symptom: a message resumed from the store rendered completely
// differently (flat, no markdown structure) than the exact same message did
// live, whenever it contained a tab-indented code block. Builds one
// scrollback by feeding the whole text in a single append (simulating
// hydrate.go replaying a stored message) and another by feeding it in small
// chunks (simulating live token-by-token streaming), and asserts both lay out
// to byte-identical rows.
func TestHydrateMatchesLiveRenderingWithTabsInText(t *testing.T) {
	text := "## Heading\n\n**bold intro**\n\n```go\nfunc f() {\n\treturn 1\n}\n```\n\n- bullet one\n- bullet two\n\nClosing paragraph."

	hydrate := newScrollback(1024)
	hydrate.append(StyledLine{Style: styleAssistant, Text: text})

	live := newScrollback(1024)
	const chunkSize = 5
	for i := 0; i < len(text); i += chunkSize {
		end := min(i+chunkSize, len(text))
		live.append(StyledLine{Style: styleAssistant, Text: text[i:end]})
	}

	width := 60
	hydRows, _ := hydrate.layoutAll(width)
	liveRows, _ := live.layoutAll(width)
	if len(hydRows) != len(liveRows) {
		t.Fatalf("row count: hydrate=%d live=%d", len(hydRows), len(liveRows))
	}
	for i := range hydRows {
		h, l := stripANSI(hydRows[i].Text), stripANSI(liveRows[i].Text)
		if h != l {
			t.Errorf("row %d differs:\n  hydrate: %q\n  live:    %q", i, h, l)
		}
	}
	// And structure must actually survive, not just match each other: both
	// empty in the same broken way would also pass the loop above.
	joined := ""
	for _, r := range hydRows {
		joined += stripANSI(r.Text)
	}
	if !strings.Contains(joined, "return 1") || !strings.Contains(joined, "bullet one") {
		var rows []string
		for _, r := range hydRows {
			rows = append(rows, stripANSI(r.Text))
		}
		t.Fatalf("hydrated render lost content, got rows: %v", rows)
	}
}
