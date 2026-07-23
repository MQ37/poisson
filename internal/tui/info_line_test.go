package tui

import (
	"bytes"
	"strings"
	"testing"
)

// TestEphemeralHintGetsOwnRowAboveKeybindings is the direct regression test
// for the reported UX bug: an ephemeral status.Hint (e.g. a mode-toggle
// notice) used to be prepended onto the same row as the keybinding list,
// which was already long enough to get truncated on realistic terminal
// widths — sometimes cutting off the hint, sometimes the mode tag, depending
// on which came first. It must now render on its own row, directly above the
// keybinding row, which itself must show its full text untouched.
func TestEphemeralHintGetsOwnRowAboveKeybindings(t *testing.T) {
	e := newTUIIntegEnv(t, nil)
	tui := e.tui
	tui.cols = 100
	tui.recomputeLayout()

	v := newVterm(tui.rows, tui.cols)
	buf := tui.writer.(*bytes.Buffer)
	tick := func() {
		tui.mu.Lock()
		tui.dirty.markFull()
		tui.mu.Unlock()
		tui.paint(tui.dirty.consume())
		v.apply(buf.String())
		buf.Reset()
	}

	// Baseline: no ephemeral hint, single row at the bottom.
	tick()
	bottom := v.visibleRow(tui.rows)
	if !strings.Contains(bottom, "Tab:conv") || !strings.Contains(bottom, "Enter:send") {
		t.Fatalf("bottom row = %q, want the full keybinding hint", bottom)
	}
	aboveBottomBefore := v.visibleRow(tui.rows - 1)

	// Trigger an ephemeral hint the same way Shift+Tab does.
	tui.mu.Lock()
	tui.toggleApprovalModeLocked()
	tui.mu.Unlock()
	tick()

	bottom = v.visibleRow(tui.rows)
	aboveBottom := v.visibleRow(tui.rows - 1)

	if strings.Contains(bottom, "paranoid mode") {
		t.Errorf("bottom row = %q, ephemeral hint must not share the keybinding row", bottom)
	}
	if !strings.Contains(bottom, "Tab:conv") || !strings.Contains(bottom, "Enter:send") {
		t.Errorf("bottom row = %q, want the full keybinding hint kept intact", bottom)
	}
	if !strings.Contains(aboveBottom, "paranoid mode") {
		t.Errorf("row above bottom = %q, want the ephemeral hint shown there", aboveBottom)
	}
	if aboveBottom == aboveBottomBefore {
		t.Error("expected the row above the keybinding line to change once the ephemeral hint appeared")
	}
}
