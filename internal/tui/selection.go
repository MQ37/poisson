package tui

import (
	"fmt"
	"time"
)

// selectionState is a mouse text selection over the conversation scrollback.
// Row/col are absolute addresses (row = index into scrollback.layoutAll's row
// list, col = 0-based rune offset into that row's stripped text), so the range
// stays valid while the user scrolls mid-drag or after the drag ends.
type selectionState struct {
	set    bool // a selection exists (dragging or finalized)
	active bool // still dragging (button held)

	anchorRow, anchorCol int
	curRow, curCol       int
}

// moved reports whether the pointer left its starting cell — used to tell a
// plain click (no movement) apart from a real drag-select.
func (s selectionState) moved() bool {
	return s.anchorRow != s.curRow || s.anchorCol != s.curCol
}

// bounds returns the selection span in reading order (top-left to
// bottom-right), regardless of which direction the drag went.
func (s selectionState) bounds() (loRow, loCol, hiRow, hiCol int) {
	if s.anchorRow < s.curRow || (s.anchorRow == s.curRow && s.anchorCol <= s.curCol) {
		return s.anchorRow, s.anchorCol, s.curRow, s.curCol
	}
	return s.curRow, s.curCol, s.anchorRow, s.anchorCol
}

// scrollAbsPosLocked maps a terminal (row,col) to an absolute scrollback
// address. ok is false when the position isn't inside the conversation area
// (header, input, pinned subagent rows). Must be called with t.mu held.
func (t *TUI) scrollAbsPosLocked(row, col int) (absRow, absCol int, ok bool) {
	scrollStart := t.headerRows + 1
	scrollEnd := t.headerRows + t.scrollRows
	if row < scrollStart || row > scrollEnd {
		return 0, 0, false
	}
	pinRows := t.convPinRows()
	vi := row - scrollStart - pinRows
	if vi < 0 {
		return 0, 0, false
	}
	viewH := t.convScrollRows()
	width := t.contentWidth()
	start := t.scroll.viewportStart(viewH, width)
	absCol = col - 1
	if absCol < 0 {
		absCol = 0
	}
	return start + vi, absCol, true
}

// beginPressLocked handles a left-button press: overlay chrome (pickers,
// completion, search) keeps its existing instant click behavior; a press over
// the conversation starts a candidate selection that beginRelease resolves
// into either a click (no movement) or a finalized text selection.
func (t *TUI) beginPressLocked(row, col int) {
	t.sel = selectionState{}
	if t.dispatchOverlayClickLocked(row) {
		return
	}
	absRow, absCol, ok := t.scrollAbsPosLocked(row, col)
	if !ok {
		return
	}
	t.sel = selectionState{set: true, active: true, anchorRow: absRow, anchorCol: absCol, curRow: absRow, curCol: absCol}
}

// extendSelectionLocked updates the drag endpoint and auto-scrolls the
// conversation when the pointer sits at the viewport edge, so the user can
// select text that has scrolled out of view without releasing the button.
func (t *TUI) extendSelectionLocked(row, col int) {
	if !t.sel.active {
		return
	}
	scrollStart := t.headerRows + 1
	scrollEnd := t.headerRows + t.scrollRows
	switch row {
	case scrollStart:
		t.scroll.scrollUp(1)
	case scrollEnd:
		t.scroll.scrollDown(1)
	}
	if absRow, absCol, ok := t.scrollAbsPosLocked(clampInt(row, scrollStart, scrollEnd), col); ok {
		t.sel.curRow, t.sel.curCol = absRow, absCol
	}
	t.markScrollDirty()
}

// endSelectionLocked finishes a drag. No movement → treat it as the original
// click-to-expand behavior; real movement → keep the selection highlighted
// until copied, cleared, or replaced.
func (t *TUI) endSelectionLocked(row int) {
	if !t.sel.active {
		return
	}
	t.sel.active = false
	if !t.sel.moved() {
		t.sel = selectionState{}
		t.handleMouseClickLocked(row)
		return
	}
	t.markScrollDirty()
}

// clearSelectionLocked drops any selection (Esc, typing, new prompt).
func (t *TUI) clearSelectionLocked() {
	if !t.sel.set {
		return
	}
	t.sel = selectionState{}
	t.markScrollDirty()
}

// copySelectionLocked copies the current selection to the system clipboard via
// OSC 52 and shows a brief confirmation hint. Always leaves a hint behind —
// even when there's nothing to copy — so Ctrl+Y is never silently a no-op
// (that made a real bug, dropped drag events leaving no selection,
// indistinguishable from the key simply not being recognized).
func (t *TUI) copySelectionLocked() {
	if !t.sel.set {
		t.setEphemeralHintLocked("Nothing selected — click and drag over the conversation first", 2*time.Second)
		return
	}
	loRow, loCol, hiRow, hiCol := t.sel.bounds()
	text := t.scroll.selectedText(t.contentWidth(), loRow, loCol, hiRow, hiCol)
	if text == "" {
		t.setEphemeralHintLocked("Selection is empty", 2*time.Second)
		return
	}
	t.writeRaw(formatOsc52(text))
	n := 1
	for i := range text {
		if text[i] == '\n' {
			n++
		}
	}
	label := "line"
	if n != 1 {
		label = "lines"
	}
	t.setEphemeralHintLocked(fmt.Sprintf("Copied %d %s to clipboard", n, label), 2*time.Second)
}
