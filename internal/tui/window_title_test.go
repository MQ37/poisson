package tui

import (
	"bytes"
	"strings"
	"testing"
)

func TestWindowTitleFor(t *testing.T) {
	cases := []struct {
		title, id string
		pending   bool
		want      string
	}{
		{"my great session", "s-a3f9c1d2", false, "px - my great session"},
		{"", "s-a3f9c1d2", false, "px - s-a3f9c1"}, // truncated to windowTitleIDLen
		{"", "s-ab", false, "px - s-ab"},           // shorter than the cap: used whole
		{"", "", false, "px"},
		{"my great session", "s-a3f9c1d2", true, "O px - my great session"},
		{"", "", true, "O px"},
	}
	for _, c := range cases {
		if got := windowTitleFor(c.title, c.id, c.pending); got != c.want {
			t.Errorf("windowTitleFor(%q, %q, %v) = %q, want %q", c.title, c.id, c.pending, got, c.want)
		}
	}
}

func TestFormatWindowTitle(t *testing.T) {
	got := formatWindowTitle("px - foo")
	want := "\x1b]0;px - foo\a"
	if got != want {
		t.Errorf("formatWindowTitle = %q, want %q", got, want)
	}
}

// TestUpdateWindowTitleLockedOnlyWritesOnChange verifies the OSC sequence is
// emitted once per actual title change, not once per paint tick (up to
// ~30/s) — repeated identical writes would spam a terminal multiplexer for
// no reason.
func TestUpdateWindowTitleLockedOnlyWritesOnChange(t *testing.T) {
	tui := newTUI(nil, "s-test", nil)
	buf := &bytes.Buffer{}
	tui.writer = buf

	tui.mu.Lock()
	tui.status.SessionID = "s-abc12345"
	tui.updateWindowTitleLocked()
	tui.updateWindowTitleLocked()
	tui.updateWindowTitleLocked()
	tui.mu.Unlock()

	out := buf.String()
	if n := strings.Count(out, "\x1b]0;"); n != 1 {
		t.Errorf("OSC title sequence written %d times for 3 identical calls, want 1: %q", n, out)
	}
	if !strings.Contains(out, "px - s-abc123") {
		t.Errorf("missing expected title text: %q", out)
	}

	buf.Reset()
	tui.mu.Lock()
	tui.status.Title = "renamed"
	tui.updateWindowTitleLocked()
	tui.mu.Unlock()

	out = buf.String()
	if !strings.Contains(out, "px - renamed") {
		t.Errorf("title change didn't re-emit the OSC sequence: %q", out)
	}
}

// TestTUIInteg_WindowTitleReflectsSessionNameAndID exercises the real path:
// paint() calls syncHeaderFromAgentLocked (pulling the title from the store)
// then updateWindowTitleLocked. Before /name, the title falls back to the
// session id; after /name, it switches to the given name. Checks the RAW
// (not stripANSI'd) buffer, since the OSC title sequence -- and the title
// text embedded in it -- is exactly what stripANSI removes.
func TestTUIInteg_WindowTitleReflectsSessionNameAndID(t *testing.T) {
	e := newTUIIntegEnv(t, nil)
	rawPaint := func() string {
		e.tui.dirty.markFull()
		e.tui.paint(e.tui.dirty.consume())
		buf := e.tui.writer.(*bytes.Buffer)
		s := buf.String()
		buf.Reset()
		return s
	}

	raw := rawPaint()
	if !strings.Contains(raw, "px - "+e.sid[:windowTitleIDLen]) {
		t.Errorf("window title should default to the session id prefix, got:\n%q", raw)
	}

	e.tui.mu.Lock()
	if err := e.tui.handleSlash("/name my great session"); err != nil {
		e.tui.mu.Unlock()
		t.Fatalf("handleSlash: %v", err)
	}
	e.tui.mu.Unlock()

	raw = rawPaint()
	if !strings.Contains(raw, "px - my great session") {
		t.Errorf("window title should reflect the new session name, got:\n%q", raw)
	}
}

// TestTUIInteg_WindowTitleShowsOWhileApprovalPending verifies the window
// title gets an "O " prefix while a bash approval is outstanding, so the
// user notices a stalled turn from a tab bar/window list even when that
// terminal isn't focused, and drops the prefix again once resolved.
func TestTUIInteg_WindowTitleShowsOWhileApprovalPending(t *testing.T) {
	e := newTUIIntegEnv(t, nil)
	rawPaint := func() string {
		e.tui.dirty.markFull()
		e.tui.paint(e.tui.dirty.consume())
		buf := e.tui.writer.(*bytes.Buffer)
		s := buf.String()
		buf.Reset()
		return s
	}

	raw := rawPaint()
	if strings.Contains(raw, "O px") {
		t.Errorf("window title should not show O before any approval is pending, got:\n%q", raw)
	}

	e.tui.approving.Store(true)
	raw = rawPaint()
	if !strings.Contains(raw, "\x1b]0;O px") {
		t.Errorf("window title should be prefixed with O while approval is pending, got:\n%q", raw)
	}

	e.tui.approving.Store(false)
	raw = rawPaint()
	if strings.Contains(raw, "O px") {
		t.Errorf("window title should drop the O prefix once approval resolves, got:\n%q", raw)
	}
}
