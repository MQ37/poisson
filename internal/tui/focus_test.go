package tui

import (
	"io"
	"strings"
	"testing"
)

func TestOffsetConvDirtyRowsIncludesPin(t *testing.T) {
	tui := newTUI(nil, "s1", nil)
	tui.focusRegion = focusConv
	got := tui.offsetConvDirtyRows([]int{0, 1, 2})
	want := []int{0, 1, 2, 3}
	if len(got) != len(want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rows[%d] = %d, want %d (%v)", i, got[i], want[i], got)
		}
	}
}

func TestConvFocusPinnedPrompt(t *testing.T) {
	tui := newTUI(nil, "s1", nil)
	tui.writer = io.Discard
	tui.rows = 24
	tui.cols = 80
	tui.scrollRows = 16
	tui.scroll.append(StyledLine{Style: styleUser, Text: "first prompt"})
	tui.scroll.append(StyledLine{Style: styleAssistant, Text: "reply"})
	tui.scroll.append(StyledLine{Style: styleUser, Text: "second prompt"})

	tui.enterConvFocus()
	if tui.focusRegion != focusConv {
		t.Fatal("expected conv focus")
	}
	if tui.convUserIdx != 1 {
		t.Fatalf("convUserIdx=%d want latest (1)", tui.convUserIdx)
	}
	line := tui.pinnedPromptLine(60)
	if !containsPlain(line, "second prompt") {
		t.Fatalf("pinned line = %q", stripANSI(line))
	}
	if !strings.Contains(line, bgBlue) {
		t.Fatalf("pinned line should use full-width background: %q", stripANSI(line))
	}
	if visibleWidth(line) != 60 {
		t.Fatalf("pinned line width = %d, want 60", visibleWidth(line))
	}
}

func TestConvFocusStepPrompts(t *testing.T) {
	tui := newTUI(nil, "s1", nil)
	tui.rows = 24
	tui.cols = 80
	tui.scrollRows = 16
	tui.scroll.append(StyledLine{Style: styleUser, Text: "alpha"})
	tui.scroll.append(StyledLine{Style: styleUser, Text: "beta"})
	tui.enterConvFocus()

	tui.stepConvPrompt(-1)
	if tui.convUserIdx != 0 {
		t.Fatalf("idx=%d want 0", tui.convUserIdx)
	}
	line := tui.pinnedPromptLine(40)
	if !containsPlain(line, "alpha") {
		t.Fatalf("pinned = %q", stripANSI(line))
	}

	tui.stepConvPrompt(1)
	if tui.convUserIdx != 1 {
		t.Fatalf("idx=%d want 1", tui.convUserIdx)
	}
}

func TestTabTogglesConvFocus(t *testing.T) {
	tui := newTUI(nil, "s1", nil)
	tui.rows = 24
	tui.cols = 80
	tui.scrollRows = 16
	tui.editor.setText("")
	tui.scroll.append(StyledLine{Style: styleUser, Text: "hello"})

	tui.handleTabKey()
	if tui.focusRegion != focusConv {
		t.Fatal("expected conv focus after Tab")
	}
	tui.handleTabKey()
	if tui.focusRegion != focusInput {
		t.Fatal("expected input focus after second Tab")
	}
}

func TestConvFocusCtrlCFallthrough(t *testing.T) {
	tui := newTUI(nil, "s1", nil)
	tui.rows = 24
	tui.cols = 80
	tui.scrollRows = 16
	tui.scroll.append(StyledLine{Style: styleUser, Text: "hello"})
	tui.enterConvFocus()

	quit, err := tui.feed([]byte{3})
	if err != nil || quit {
		t.Fatalf("first Ctrl+C in conv focus: quit=%v err=%v", quit, err)
	}
	if tui.status.Hint == "" {
		t.Fatal("expected exit hint after Ctrl+C in conv focus")
	}
}

func containsPlain(hay, needle string) bool {
	return strings.Contains(stripANSI(hay), needle)
}