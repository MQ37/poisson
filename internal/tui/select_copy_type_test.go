package tui

import (
	"bytes"
	"testing"
)

// TestDuplicateSeparatorAfterSelectCopyThenType reproduces a live user
// report, root-caused via a real captured session replayed through pyte (a
// real VT100/xterm emulator): renderHintLine() was the only line in
// paintInputRegion never truncated to the terminal width. Its base text
// alone (160 runes) fits comfortably on this test's 185-column terminal —
// matching the user's own real terminal size — but the ephemeral "Copied N
// lines to clipboard" hint Ctrl+Y prepends pushes the combined string past
// 185, so a real terminal auto-wraps it and, with no scroll region
// configured, scrolls the WHOLE SCREEN up by one — corrupting every other
// absolute-row-addressed write already on screen. Repeated once per
// keystroke for as long as the ephemeral hint is still showing (2s TTL),
// which is exactly why it looked like the separator kept duplicating.
//
// Uses the same vterm tick-by-tick replay as TestDuplicateSeparatorOnInput-
// Shrink; vterm now auto-wraps and scrolls exactly like a real terminal (an
// earlier, naive version of it — which never wrapped — was blind to this
// entire bug class).
func TestDuplicateSeparatorAfterSelectCopyThenType(t *testing.T) {
	tui := newTUI(nil, "s-test", nil)
	tui.rows = 47
	tui.cols = 185
	buf := &bytes.Buffer{}
	tui.writer = buf
	tui.recomputeLayout()
	tui.scroll.append(StyledLine{Style: styleAssistant, Text: "hello world this is selectable text"})
	v := newVterm(tui.rows, tui.cols)

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
