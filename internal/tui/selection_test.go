package tui

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
)

func TestSelectionStateMovedAndBounds(t *testing.T) {
	s := selectionState{anchorRow: 2, anchorCol: 5, curRow: 2, curCol: 5}
	if s.moved() {
		t.Fatal("same cell should not report moved")
	}
	s.curCol = 9
	if !s.moved() {
		t.Fatal("different column should report moved")
	}

	// Backward drag (cur above/left of anchor) still normalizes to reading order.
	s = selectionState{anchorRow: 4, anchorCol: 10, curRow: 1, curCol: 2}
	loRow, loCol, hiRow, hiCol := s.bounds()
	if loRow != 1 || loCol != 2 || hiRow != 4 || hiCol != 10 {
		t.Fatalf("bounds = (%d,%d)-(%d,%d), want (1,2)-(4,10)", loRow, loCol, hiRow, hiCol)
	}
}

func newSelectionTestTUI() *TUI {
	tui := newTUI(nil, "s1", nil)
	tui.headerRows = 1
	tui.scrollRows = 6
	tui.cols = 40
	tui.writer = &bytes.Buffer{}
	return tui
}

func TestMouseDragCreatesSelectionNotClick(t *testing.T) {
	tui := newSelectionTestTUI()
	tui.scroll.appendBlock(blockThinking, "thought")
	tui.scroll.finalizeThinking()

	// Press then move then release: should NOT toggle expand, should leave a
	// finalized selection instead.
	tui.handleMouseInput([]byte("\x1b[<0;3;2M"))  // press row2 col3
	tui.handleMouseInput([]byte("\x1b[<32;6;2M")) // drag motion to col6 (btn0|32)
	tui.handleMouseInput([]byte("\x1b[<0;6;2m"))  // release
	if tui.scroll.blocks[0].meta.Collapsed != true {
		t.Fatal("drag must not toggle thinking collapse")
	}
	if !tui.sel.set || tui.sel.active {
		t.Fatalf("expected finalized selection, got %+v", tui.sel)
	}
}

func TestMouseClickNoMovementStillTogglesExpand(t *testing.T) {
	tui := newSelectionTestTUI()
	tui.scroll.appendBlock(blockThinking, "thought")
	tui.scroll.finalizeThinking()

	tui.handleMouseInput([]byte("\x1b[<0;3;2M")) // press
	tui.handleMouseInput([]byte("\x1b[<0;3;2m")) // release, same cell
	if tui.scroll.blocks[0].meta.Collapsed {
		t.Fatal("plain click should expand")
	}
	if tui.sel.set {
		t.Fatal("plain click should not leave a selection")
	}
}

func TestDragAutoScrollsAtViewportEdge(t *testing.T) {
	tui := newSelectionTestTUI()
	tui.scroll.appendRaw(styleAssistant, strings.Repeat("word ", 400))
	tui.scroll.scrollToBottom()

	topRow := tui.headerRows + 1
	tui.handleMouseInput([]byte("\x1b[<0;3;" + strconv.Itoa(topRow) + "M"))
	before := tui.scroll.scrollOffset
	// Drag motion pinned at the top edge row should scroll up.
	tui.handleMouseInput([]byte("\x1b[<32;3;" + strconv.Itoa(topRow) + "M"))
	if tui.scroll.scrollOffset <= before {
		t.Fatalf("expected scrollOffset to increase from %d, got %d", before, tui.scroll.scrollOffset)
	}
}

func TestCopySelectionWritesOSC52(t *testing.T) {
	tui := newSelectionTestTUI()
	tui.scroll.appendRaw(styleAssistant, "hello world")

	tui.sel = selectionState{set: true, anchorRow: 0, anchorCol: 0, curRow: 0, curCol: 4}
	buf := &bytes.Buffer{}
	tui.writer = buf
	tui.mu.Lock()
	tui.copySelectionLocked()
	tui.mu.Unlock()

	out := buf.String()
	if !strings.HasPrefix(out, "\x1b]52;c;") {
		t.Fatalf("expected OSC 52 sequence, got %q", out)
	}
	if tui.status.Hint == "" {
		t.Fatal("expected a copy confirmation hint")
	}
}

func TestCtrlYCopiesSelection(t *testing.T) {
	tui := newSelectionTestTUI()
	tui.scroll.appendRaw(styleAssistant, "abcdef")
	tui.sel = selectionState{set: true, anchorRow: 0, anchorCol: 0, curRow: 0, curCol: 2}
	buf := &bytes.Buffer{}
	tui.writer = buf

	tui.feedKey(Key{Kind: KeyCtrl, Byte: 25})

	if !strings.HasPrefix(buf.String(), "\x1b]52;c;") {
		t.Fatalf("Ctrl+Y should copy via OSC52, got %q", buf.String())
	}
}

func TestPlainCtrlCDoesNotCopy(t *testing.T) {
	tui := newSelectionTestTUI()
	tui.sel = selectionState{set: true, anchorRow: 0, anchorCol: 0, curRow: 0, curCol: 2}
	buf := &bytes.Buffer{}
	tui.writer = buf

	if !(Key{Kind: KeyCtrl, Byte: 3}).isCtrlC() {
		t.Fatal("sanity: plain Ctrl+C should still be isCtrlC")
	}
	if (Key{Kind: KeyCtrl, Byte: 3}).isCtrlY() {
		t.Fatal("Ctrl+C must not also be treated as the copy shortcut")
	}
}

func TestAnyOtherKeyClearsSelection(t *testing.T) {
	tui := newSelectionTestTUI()
	tui.sel = selectionState{set: true, anchorRow: 0, anchorCol: 0, curRow: 0, curCol: 2}

	tui.feedKey(Key{Kind: KeyRune, Rune: 'x'})

	if tui.sel.set {
		t.Fatal("typing should clear the selection")
	}
}

func TestScrollbackSelectedText(t *testing.T) {
	s := newScrollback(1024)
	s.appendRaw(styleAssistant, "hello world")
	got := s.selectedText(80, 0, 0, 0, 4)
	if got != "hello" {
		t.Fatalf("selectedText = %q, want %q", got, "hello")
	}
}

// TestDragSurvivesSplitRead reproduces the real-world failure: a fast drag
// floods motion reports, and a stdin read() can return a prefix cut mid
// escape-sequence. Without carry-over buffering, the whole burst (including
// complete events already in that read) used to silently vanish.
func TestDragSurvivesSplitRead(t *testing.T) {
	tui := newSelectionTestTUI()
	tui.scroll.appendRaw(styleAssistant, "hello world")

	full := "\x1b[<0;3;2M"    // press row2 col3
	motion := "\x1b[<32;8;2M" // drag motion to col8
	if !tui.handleMouseInput([]byte(full)) {
		t.Fatal("press should be handled")
	}
	// Split the motion report across two reads, right in the middle. The
	// first (incomplete) half is still "handled" — it's buffered, not
	// dropped or passed through to the keyboard decoder.
	split := len(motion) / 2
	if !tui.handleMouseInput([]byte(motion[:split])) {
		t.Fatal("an incomplete but mouse-shaped prefix should report handled=true (buffered)")
	}
	if !tui.handleMouseInput([]byte(motion[split:])) {
		t.Fatal("second half should complete the buffered sequence")
	}
	if tui.sel.curCol != 7 {
		t.Fatalf("curCol = %d, want 7 (drag split across reads must still apply)", tui.sel.curCol)
	}

	release := "\x1b[<0;8;2m"
	tui.handleMouseInput([]byte(release))
	if !tui.sel.set || tui.sel.active {
		t.Fatalf("expected finalized selection after split-read drag, got %+v", tui.sel)
	}
}

func TestDragSplitAcrossThreeReads(t *testing.T) {
	tui := newSelectionTestTUI()
	tui.scroll.appendRaw(styleAssistant, "hello world")

	tui.handleMouseInput([]byte("\x1b[<0;3;2M"))
	motion := "\x1b[<32;9;2M"
	a, b := motion[:3], motion[3:6]
	c := motion[6:]
	tui.handleMouseInput([]byte(a))
	tui.handleMouseInput([]byte(b))
	tui.handleMouseInput([]byte(c))
	if tui.sel.curCol != 8 {
		t.Fatalf("curCol = %d, want 8 after 3-way split motion event", tui.sel.curCol)
	}
}

func TestConsumeMouseEventsMixedCompleteAndPartial(t *testing.T) {
	complete := "\x1b[<0;3;2M"
	partial := "\x1b[<32;5"
	events, rest := consumeMouseEvents([]byte(complete + partial))
	if len(events) != 1 {
		t.Fatalf("expected 1 complete event, got %d", len(events))
	}
	if string(rest) != partial {
		t.Fatalf("rest = %q, want leftover partial %q", rest, partial)
	}
}

func TestMouseBufDroppedWhenNotMouseShaped(t *testing.T) {
	tui := newSelectionTestTUI()
	tui.handleMouseInput([]byte("\x1b[<0;3;2M")) // press, complete
	tui.handleMouseInput([]byte("\x1b[<0;3;2m")) // release, complete
	if tui.mouseBuf != nil {
		t.Fatalf("mouseBuf should be empty after two complete events, got %q", tui.mouseBuf)
	}
}

// TestHighlightSurvivesEmbeddedReset reproduces the reported bug: a code span
// (or any inline styling) embeds its own SGR reset, which used to cancel the
// highlight early and leave the rest of the span unhighlighted.
func TestHighlightSurvivesEmbeddedReset(t *testing.T) {
	// "abc" + styled "DEF" (with its own reset) + "ghi", highlighting all of it.
	line := "abc\x1b[36mDEF\x1b[0mghi"
	out := highlightPlainSpans(line, [][2]int{{0, 9}}, reverseVideo, reset)
	plain := stripANSI(out)
	if plain != "abcDEFghi" {
		t.Fatalf("plain text corrupted: %q", plain)
	}
	// After the embedded reset, reverseVideo must be re-asserted so "ghi"
	// (up to the real span end) stays highlighted instead of going bare.
	idx := strings.Index(out, "ghi")
	if idx < len(reverseVideo) || out[idx-len(reverseVideo):idx] != reverseVideo {
		t.Fatalf("reverseVideo not re-asserted right before ghi: %q", out)
	}
}

// TestHighlightSpanOpeningOnChainedEscapes covers the exact regression hit
// while fixing the embedded-reset bug: a span that starts right where the
// line has several back-to-back escapes (e.g. color+bold, as bashDangerStyle
// emits) must not get pre written more than once before the first visible
// character.
func TestHighlightSpanOpeningOnChainedEscapes(t *testing.T) {
	line := "\x1b[31m\x1b[1mrm\x1b[0m ok"
	out := highlightPlainSpans(line, [][2]int{{0, 2}}, "[", "]")
	plain := stripANSI(out)
	if plain != "[rm] ok" {
		t.Fatalf("got %q, want \"[rm] ok\" (pre must appear exactly once)", plain)
	}
}
