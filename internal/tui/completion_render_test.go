package tui

import (
	"strings"
	"testing"
)

func TestCompletionLinesCapsToScrollRows(t *testing.T) {
	tui := newTUI(nil, "s-abc", nil)
	tui.scrollRows = 5
	cands := make([]string, 20)
	for i := range cands {
		cands[i] = "/cmd"
	}
	c := &completion{kind: completionSlash, cands: cands, idx: -1}
	lines := completionLines(tui, c)
	if len(lines) != 5 {
		t.Fatalf("lines = %d, want 5", len(lines))
	}
	if !strings.Contains(lines[0], "commands") {
		t.Fatalf("header = %q", lines[0])
	}
}

func TestPaintCompletionZoneRestoresShrunkRows(t *testing.T) {
	tui := newTUI(nil, "s-abc", nil)
	tui.cols = 40
	tui.rows = 12
	tui.scrollRows = 8
	for i := 0; i < 8; i++ {
		tui.scroll.appendRaw(styleSystem, "scroll-"+string(rune('0'+i)))
	}
	lay := tui.prepareLayout()

	big := completionLines(tui, &completion{
		kind:  completionSlash,
		cands: []string{"/a", "/b", "/c", "/d", "/e", "/f"},
		idx:   -1,
	})
	tui.lastCompletionRows = len(big)
	var first strings.Builder
	tui.paintCompletionZone(&first, lay, big, len(big))

	small := completionLines(tui, &completion{
		kind:  completionSlash,
		cands: []string{"/a"},
		idx:   -1,
	})
	var second strings.Builder
	tui.paintCompletionZone(&second, lay, small, len(small))

	out := second.String()
	if strings.Contains(out, "commands (6)") {
		t.Fatalf("stale large header still present in %q", out)
	}
	if strings.Count(out, "commands (1)") != 1 {
		t.Fatalf("expected one small header in %q", out)
	}
	_ = first
}
