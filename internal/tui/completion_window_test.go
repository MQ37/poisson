package tui

import "testing"

func TestCompletionLinesWindowsAroundSelection(t *testing.T) {
	tui := newTUI(nil, "s1", nil)
	tui.scrollRows = 6
	cands := make([]string, 20)
	for i := range cands {
		cands[i] = "/cmd" + string(rune('a'+i))
	}
	tui.completion = &completion{
		kind:  completionSlash,
		cands: cands,
		idx:   15,
	}
	lines := completionLines(tui, tui.completion)
	if len(lines) > tui.scrollRows {
		t.Fatalf("too many lines: %d", len(lines))
	}
	body := lines[1:]
	found := false
	for _, ln := range body {
		if containsPlain(ln, cands[15]) {
			found = true
			if !containsPlain(ln, "▶") {
				t.Fatalf("selected line should have marker: %q", ln)
			}
		}
	}
	if !found {
		t.Fatalf("selected candidate missing from window: %v", body)
	}
}