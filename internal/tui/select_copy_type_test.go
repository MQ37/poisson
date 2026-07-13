package tui

import (
	"bytes"
	"testing"
)

// TestDuplicateSeparatorAfterSelectCopyThenType reproduces a live user report:
// select text in the conversation (mouse drag), press Ctrl+Y to copy, then
// start typing — the input separator duplicates several times, stacking up
// on screen. Uses the same vterm tick-by-tick replay as
// TestDuplicateSeparatorOnInputShrink, so any leftover un-redrawn row shows
// up exactly as it would on a real terminal.
func TestDuplicateSeparatorAfterSelectCopyThenType(t *testing.T) {
	tui := newTUI(nil, "s-test", nil)
	tui.rows = 24
	tui.cols = 80
	buf := &bytes.Buffer{}
	tui.writer = buf
	tui.recomputeLayout()
	tui.scroll.append(StyledLine{Style: styleAssistant, Text: "hello world this is selectable text"})
	v := newVterm(tui.rows)

	tick := func() {
		tui.paint(tui.dirty.consume())
		v.apply(buf.String())
		buf.Reset()
	}

	tui.dirty.markFull()
	tick()

	// Simulate a real mouse drag-select over the seeded line: press, drag,
	// release, exactly through the same entry points a real drag would use.
	tui.mu.Lock()
	tui.beginPressLocked(tui.headerRows+1, 1)
	tui.extendSelectionLocked(tui.headerRows+1, 12)
	tui.endSelectionLocked(tui.headerRows + 1)
	tui.mu.Unlock()
	tick()

	// Ctrl+Y: copy the selection (byte 0x19, exactly what a real terminal
	// sends for that chord).
	if _, err := tui.feed([]byte{0x19}); err != nil {
		t.Fatal(err)
	}
	tick()

	max := 0
	for _, r := range "hello, does this glitch?" {
		if _, err := tui.feed([]byte(string(r))); err != nil {
			t.Fatal(err)
		}
		tick()
		if n := countSeparatorRows(v); n > max {
			max = n
		}
	}
	if max > 1 {
		t.Errorf("up to %d separator rows visible simultaneously after select+copy+type, want at most 1", max)
	}
}
