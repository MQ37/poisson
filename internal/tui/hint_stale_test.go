package tui

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"poisson/internal/provider"
)

// TestFooterHintNotStaleAfterTurnCompletes reproduces a real user-reported
// bug: after a turn finishes, the footer hint line kept showing the "busy"
// hint ("Enter:queue message · ...") instead of the idle one ("Enter:send ·
// ..."), until some unrelated keypress (e.g. Esc) forced a repaint.
//
// Root cause: startTurn's completion path (agent_io.go) flips
// t.status.Thinking to false and calls only t.dirty.markStatus() — but
// renderHintLine() is painted inside paintInputRegion, which only runs when
// snap.input (or snap.overlay) is set (see paintPartial in render_v2.go).
// markStatus() alone repaints the header, never the footer. The reverse
// transition (idle -> busy, in submit()) happens to look fine only because
// clearing the editor's text on submit already dirties input for unrelated
// reasons.
//
// This test drives real keystrokes through feedKey (so submit() and the real
// startTurn goroutine run exactly as in production), replays the actual
// bytes written on each render tick onto a vterm exactly as a real terminal
// would, and checks what the footer says once the turn has genuinely
// finished — using only the dirty flags the code itself set, with no
// artificial markFull() to paper over the bug.
func TestFooterHintNotStaleAfterTurnCompletes(t *testing.T) {
	e := newTUIIntegEnv(t, [][]provider.StreamEvent{
		provider.FakeTextResponse("done", nil),
	})
	v := newVterm(e.tui.rows, e.tui.cols)
	buf := e.tui.writer.(*bytes.Buffer)

	tick := func() {
		snap := e.tui.dirty.consume()
		if snap.any() {
			e.tui.paint(snap)
		}
		v.apply(buf.String())
		buf.Reset()
	}

	// Initial full paint, same as Run()'s startup sequence.
	e.tui.mu.Lock()
	e.tui.dirty.markFull()
	e.tui.mu.Unlock()
	tick()

	footer := func() string { return v.visibleRow(e.tui.rows) }
	if strings.Contains(footer(), "queue message") {
		t.Fatalf("footer shows busy hint before any turn started: %q", footer())
	}

	// Type "hi" and press Enter, exactly like a real keystroke stream.
	for _, r := range "hi" {
		e.tui.feedKey(Key{Kind: KeyRune, Rune: r})
	}
	e.tui.feedKey(Key{Kind: KeyEnter})
	tick()

	if !strings.Contains(footer(), "queue message") {
		t.Fatalf("footer should show the busy hint while the turn is running, got %q", footer())
	}

	// Wait for the real startTurn goroutine to finish (it flips Thinking to
	// false in its own defer, independent of the render loop).
	deadline := time.Now().Add(2 * time.Second)
	for {
		e.tui.mu.Lock()
		thinking := e.tui.status.Thinking
		e.tui.mu.Unlock()
		if !thinking {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("turn never finished (status.Thinking stuck true)")
		}
		time.Sleep(time.Millisecond)
	}

	// This is the render loop's normal per-tick behavior: consume whatever
	// dirty flags are ACTUALLY set (no markFull()), and paint only that.
	tick()

	if strings.Contains(footer(), "queue message") {
		t.Errorf("footer still shows the busy hint after the turn finished: %q (want the idle Enter:send hint)", footer())
	}
	if !strings.Contains(footer(), "Enter:send") {
		t.Errorf("footer should show the idle hint after the turn finished, got %q", footer())
	}
}
