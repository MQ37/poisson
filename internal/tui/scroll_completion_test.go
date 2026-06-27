package tui

import (
	"strings"
	"testing"
)

func TestPaintPartialScrollRepaintsCompletion(t *testing.T) {
	tui := newTUIv2(nil, "s-abc", nil)
	tui.cols = 40
	tui.rows = 12
	tui.scrollRows = 8
	tui.writer = &strings.Builder{}
	for i := 0; i < 8; i++ {
		tui.scroll.appendRaw(styleSystem, "scroll-"+string(rune('0'+i)))
	}
	tui.completion = &completion{
		kind:  completionSlash,
		cands: []string{"/help", "/quit"},
		idx:   0,
	}
	lay := tui.prepareLayout()
	tui.lastCompletionRows = len(completionLines(tui, tui.completion))

	tui.paintPartial(dirtySnapshot{scroll: []int{7}}, lay)
	out := tui.writer.(*strings.Builder).String()
	if !strings.Contains(out, "commands") {
		t.Fatalf("partial scroll repaint should restore completion dropdown, got %q", out)
	}
}

func TestStreamViewportDirtyUsesRichLayout(t *testing.T) {
	s := newScrollback(1024)
	s.append(StyledLine{Style: styleAssistant, Text: "# Title\n"})
	rows := s.streamViewportDirty(10, 40)
	if len(rows) < 2 {
		t.Fatalf("markdown title should dirty multiple viewport rows, got %v", rows)
	}
}