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
	if !strings.Contains(line, fgYellow) {
		t.Fatalf("pinned turn label should use high-contrast yellow on dark theme: %q", stripANSI(line))
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

// TestConvFocusStepPromptsMultiLineMessage is a regression test: a single
// multi-line submitted message must count as exactly one turn for
// Shift+Left/Right navigation, not one turn per line it happens to wrap
// across in the source text.
func TestConvFocusStepPromptsMultiLineMessage(t *testing.T) {
	tui := newTUI(nil, "s1", nil)
	tui.rows = 24
	tui.cols = 80
	tui.scrollRows = 16
	tui.scroll.append(StyledLine{Style: styleUser, Text: "first line\nsecond line\nthird line"})
	tui.scroll.append(StyledLine{Style: styleAssistant, Text: "reply"})
	tui.scroll.append(StyledLine{Style: styleUser, Text: "a single-line follow-up"})
	tui.enterConvFocus()

	idxs := tui.scroll.userBlockIndices()
	if len(idxs) != 2 {
		t.Fatalf("userBlockIndices = %v, want exactly 2 turns (one 3-line message + one 1-line message)", idxs)
	}

	tui.stepConvPrompt(-1)
	if tui.convUserIdx != 0 {
		t.Fatalf("idx=%d want 0 (the multi-line message)", tui.convUserIdx)
	}
	line := tui.pinnedPromptLine(60)
	if !containsPlain(line, "turn 1/2") {
		t.Fatalf("pinned line = %q, want turn 1/2", stripANSI(line))
	}
	// The pinned header is a single fixed-height row: an embedded newline must
	// be flattened, not left as a literal byte that would move the cursor and
	// corrupt the row.
	if strings.Contains(stripANSI(line), "\n") {
		t.Fatalf("pinned line must not contain a raw newline: %q", stripANSI(line))
	}
	if !containsPlain(line, "first line second line third line") {
		t.Fatalf("pinned line = %q, want the multi-line message flattened to one line", stripANSI(line))
	}

	tui.stepConvPrompt(1)
	if tui.convUserIdx != 1 {
		t.Fatalf("idx=%d want 1 (the single-line follow-up) — one Shift+Right press must skip the WHOLE multi-line message, not just one of its lines", tui.convUserIdx)
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

func TestFillWidthBGPadsFullWidth(t *testing.T) {
	content := bold + fgYellow + "turn 1/2" + reset
	line := fillWidthBG(bgBlue, content, 40)
	if visibleWidth(line) != 40 {
		t.Fatalf("visible width = %d, want 40", visibleWidth(line))
	}
	// Leading band + re-apply before padding (content ends with reset).
	if strings.Count(line, bgBlue) < 2 {
		t.Fatalf("expected bg re-applied before padding: %q", line)
	}
}
