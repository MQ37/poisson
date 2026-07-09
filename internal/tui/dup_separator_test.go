package tui

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// countSeparatorRows counts rows on the virtual screen whose content is a
// long run of the chrome separator glyph (dash, or dot in conv-focus mode).
func countSeparatorRows(v *vterm) int {
	n := 0
	for row := 1; row < len(v.rows); row++ {
		line := v.visibleRow(row)
		if strings.Count(line, "─") > 10 || strings.Count(line, "·") > 10 {
			n++
		}
	}
	return n
}

// runInputStress drives one keystroke-by-keystroke sequence (each followed
// by its own render tick, i.e. the worst case where the render loop keeps up
// with every keystroke) through a preconfigured TUI, replaying every tick's
// raw bytes onto a virtual terminal exactly as a real one would, and returns
// the max number of separator rows ever simultaneously visible.
func runInputStress(t *testing.T, configure func(tui *TUI)) int {
	t.Helper()
	tui := newTUI(nil, "s-test", nil)
	tui.rows = 24
	tui.cols = 80
	buf := &bytes.Buffer{}
	tui.writer = buf
	tui.recomputeLayout()
	if configure != nil {
		configure(tui)
	}
	v := newVterm(tui.rows)

	tui.dirty.markFull()
	tui.paint(tui.dirty.consume())
	v.apply(buf.String())
	buf.Reset()

	max := 0
	tick := func() {
		tui.paint(tui.dirty.consume())
		v.apply(buf.String())
		buf.Reset()
		if n := countSeparatorRows(v); n > max {
			max = n
		}
	}

	// A big paste (a multi-row jump in one tick), then bulk-delete (another
	// multi-row jump back) — one whole chunk fed per tick, not byte by byte.
	big := strings.Repeat("word ", 60)
	if _, err := tui.feed([]byte(big)); err != nil {
		t.Fatal(err)
	}
	tick()
	if _, err := tui.feed(bytes.Repeat([]byte{127}, len(big))); err != nil {
		t.Fatal(err)
	}
	tick()

	// Then a byte-by-byte grow/shrink cycle: type long enough to wrap across
	// several lines (growing the input box), then backspace back down
	// (shrinking it) — one keystroke, one tick, repeated.
	keys := []byte(strings.Repeat("word ", 40))
	for i := 0; i < len(keys); i++ {
		if _, err := tui.feed(keys[i : i+1]); err != nil {
			t.Fatal(err)
		}
		tick()
	}
	for i := 0; i < len(keys)-5; i++ {
		if _, err := tui.feed([]byte{127}); err != nil {
			t.Fatal(err)
		}
		tick()
	}
	return max
}

// TestDuplicateSeparatorOnInputShrink is a regression test for a real bug: a
// keystroke that shrinks the input box (e.g. backspacing across a wrap
// boundary) could leave the OLD separator row on screen, undrawn, while the
// NEW (lower) separator also appeared — a duplicated "───" line above the
// input, visible for one render tick.
//
// Root cause: prepareLayout, run from inside paint(), detects the input
// height changed and calls dirty.markFull() to force a full repaint — but
// that only takes effect on the NEXT dirty.consume(), one tick too late for
// the paint() call currently running. That call proceeds as a partial
// repaint using the NEW (shrunk) geometry; the old separator's row, now just
// above the new one, falls outside both the new input region (which starts
// lower) and the untouched scroll region, and is never redrawn.
//
// Exercised under several "weirdly scrolled" conditions the bug report
// couldn't pin down precisely: plain typing, queued messages present (which
// also affect input height), scrolled up in the conversation, and
// conv-focus mode (Tab-scrolled into history) — all funnel through the same
// prepareLayout/paint path, so the fix (TUI.layoutJustChanged) covers all of
// them identically.
func TestDuplicateSeparatorOnInputShrink(t *testing.T) {
	cases := map[string]func(tui *TUI){
		"plain typing": nil,
		"queued messages present": func(tui *TUI) {
			tui.queued = []string{"first queued", "second queued", "third queued", "fourth queued"}
		},
		"scrolled up in conversation": func(tui *TUI) {
			for i := 0; i < 30; i++ {
				tui.scroll.append(StyledLine{Style: styleAssistant, Text: fmt.Sprintf("line %d of a long reply", i)})
			}
			tui.scroll.scrollUp(10)
		},
		"conv focus mode": func(tui *TUI) {
			tui.scroll.append(StyledLine{Style: styleUser, Text: "hello"})
			tui.focusRegion = focusConv
		},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if max := runInputStress(t, cfg); max > 1 {
				t.Errorf("%s: up to %d separator rows visible simultaneously, want at most 1", name, max)
			}
		})
	}
}
