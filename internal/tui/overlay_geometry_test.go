package tui

import (
	"bytes"
	"testing"

	"poisson/internal/agent"
	"poisson/internal/config"
	"poisson/internal/provider"
	"poisson/internal/store"
	"poisson/internal/testutil"
	"poisson/internal/tools"
)

// newGeometryTestTUI builds a TUI backed by a real (FakeProvider) agent —
// needed because openEffortPicker/cmdEffort read from t.agent — wired to a
// virtual terminal so tests can assert on what's actually visible on screen
// after each tick, the same way dup_separator_test.go's stress tests do.
func newGeometryTestTUI(t *testing.T, sid string) (*TUI, *vterm, func()) {
	t.Helper()
	dir := testutil.TempDir(t)
	st, err := store.Open(dir + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	st.CreateSession(&store.Session{ID: sid, Cwd: ".", Provider: "fake", Model: "m"})
	a := agent.NewAgent(st, provider.NewFakeProvider("fake", nil), tools.NewRegistry(),
		config.DefaultConfig(), sid, nil, nil)

	tui := newTUI(a, sid, nil)
	tui.rows = 24
	tui.cols = 80
	buf := &bytes.Buffer{}
	tui.writer = buf
	tui.recomputeLayout()
	v := newVterm(tui.rows, tui.cols)

	tick := func() {
		// paint locks t.mu internally — must not also be held by the caller,
		// or this self-deadlocks (sync.Mutex isn't reentrant).
		tui.paint(tui.dirty.consume())
		v.apply(buf.String())
		buf.Reset()
	}
	tui.dirty.markFull()
	tick()
	return tui, v, tick
}

// TestNoDuplicateSeparatorAfterCancelThenEffortPicker reproduces a reported
// sequence: cancel a running turn (Ctrl+C), then open and use the effort
// picker (Ctrl+L) — both transitions change what's drawn around the input
// area (the picker overlay, the "cancelled" hint), on top of the same
// input-height geometry checked by TestDuplicateSeparatorOnInputShrink.
func TestNoDuplicateSeparatorAfterCancelThenEffortPicker(t *testing.T) {
	tui, v, tick := newGeometryTestTUI(t, "cancel-effort-test")

	tui.mu.Lock()
	tui.status.Thinking = true
	tui.mu.Unlock()
	tick()

	tui.mu.Lock()
	tui.cancelActiveRunLocked()
	// Mirrors startTurn's deferred cleanup, which normally runs on the
	// worker goroutine once it notices the cancellation.
	tui.status.Thinking = false
	tui.dirty.markStatus()
	tui.mu.Unlock()
	tick()

	max := 0
	track := func() {
		if n := countSeparatorRows(v); n > max {
			max = n
		}
	}
	track()

	if _, err := tui.feed([]byte{12}); err != nil { // Ctrl+L
		t.Fatal(err)
	}
	tick()
	track()

	if _, err := tui.feed([]byte{'\r'}); err != nil { // pick the highlighted (current) item
		t.Fatal(err)
	}
	tick()
	track()
	tick() // settle
	track()

	if max > 1 {
		for row := 1; row <= tui.rows; row++ {
			t.Logf("row %2d: %q", row, v.visibleRow(row))
		}
		t.Errorf("up to %d separator rows visible simultaneously, want at most 1", max)
	}
}

// TestNoDuplicateSeparatorAfterCancelThenSlashEffort tries /effort instead of
// the Ctrl+L picker — a different code path (submit -> handleSlash) that also
// clears the editor (a command line shrinking back to empty) right after a
// cancel that dropped a queued message.
func TestNoDuplicateSeparatorAfterCancelThenSlashEffort(t *testing.T) {
	tui, v, tick := newGeometryTestTUI(t, "cancel-slash-effort-test")

	tui.mu.Lock()
	tui.status.Thinking = true
	tui.queued = []string{"queued while busy"}
	tui.mu.Unlock()
	tick()

	tui.mu.Lock()
	tui.cancelActiveRunLocked() // drops the queue, may itself markFull
	tui.status.Thinking = false
	tui.dirty.markStatus()
	tui.mu.Unlock()
	tick()

	max := 0
	track := func() {
		if n := countSeparatorRows(v); n > max {
			max = n
		}
	}
	track()

	for _, ch := range []byte("/effort high") {
		if _, err := tui.feed([]byte{ch}); err != nil {
			t.Fatal(err)
		}
		tick()
		track()
	}
	if _, err := tui.feed([]byte{'\r'}); err != nil {
		t.Fatal(err)
	}
	tick()
	track()
	tick() // settle
	track()

	if max > 1 {
		for row := 1; row <= tui.rows; row++ {
			t.Logf("row %2d: %q", row, v.visibleRow(row))
		}
		t.Errorf("up to %d separator rows visible simultaneously, want at most 1", max)
	}
}
