package tui

import (
	"testing"
	"time"

	"github.com/mq37/poisson/internal/store"
)

func TestDecoderKittyCtrlS(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
	}{
		{"c0 code", []byte{27, '[', '1', '9', ';', '5', 'u'}},
		{"letter s", []byte{27, '[', '1', '1', '5', ';', '5', 'u'}},
		{"plain xoff", []byte{19}},
	}
	for _, c := range cases {
		var d Decoder
		keys := d.Push(c.in)
		if len(keys) != 1 {
			t.Fatalf("%s: keys=%v want 1", c.name, keys)
		}
		if keys[0].Kind != KeyCtrl || keys[0].Byte != 19 {
			t.Fatalf("%s: got %+v want Ctrl+S (byte 19)", c.name, keys[0])
		}
	}
}

func TestFeedKeyCtrlLOpensEffortPicker(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)
	_, err := tui.feedKey(Key{Kind: KeyCtrl, Byte: 12})
	if err != nil {
		t.Fatal(err)
	}
	if tui.activeOverlay == nil {
		t.Fatal("expected effort picker overlay")
	}
}

func TestFeedKeyCtrlTTogglesThinking(t *testing.T) {
	tui := newTestTUIHelper()
	tui.scroll.appendBlock(blockThinking, "secret reasoning")
	tui.scroll.finalizeThinking()
	if !tui.scroll.blocks[0].meta.Collapsed {
		t.Fatal("expected collapsed thinking")
	}

	_, err := tui.feedKey(Key{Kind: KeyCtrl, Byte: 20})
	if err != nil {
		t.Fatal(err)
	}
	if tui.scroll.blocks[0].meta.Collapsed {
		t.Fatal("Ctrl+T should expand last thinking block")
	}
}

func TestFeedKeyCtrlSOpensSessionPicker(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)
	_, err := tui.feedKey(Key{Kind: KeyCtrl, Byte: 19})
	if err != nil {
		t.Fatal(err)
	}
	if tui.activeOverlay == nil {
		t.Fatal("expected session picker overlay")
	}
}

// TestFeedKeyCtrlNFiltersNamedSessionsInPicker locks in that Ctrl+N reaches
// the session picker overlay through the real dispatch path (feedKey), not
// just the overlay's own feedKey in isolation — and that it doesn't leak
// through to the global Ctrl+N history-navigation binding while the picker
// is open.
func TestFeedKeyCtrlNFiltersNamedSessionsInPicker(t *testing.T) {
	st, a, sessionID := newTestStoreAndAgent(t)
	if err := st.SetSessionTitle(sessionID, "named one"); err != nil {
		t.Fatal(err)
	}
	unnamed := store.NewSessionID()
	st.CreateSession(&store.Session{
		ID: unnamed, Cwd: "/tmp", Provider: "ollama", Model: "test",
		CreatedAt: 1, UpdatedAt: 1,
	})
	tui := newTUIWithAgent(a, sessionID)
	if _, err := tui.feedKey(Key{Kind: KeyCtrl, Byte: 19}); err != nil { // Ctrl+S
		t.Fatal(err)
	}
	ov, ok := tui.activeOverlay.(*filterableListOverlay)
	if !ok {
		t.Fatalf("expected session picker overlay, got %T", tui.activeOverlay)
	}
	if len(ov.filtered()) != 2 {
		t.Fatalf("expected both sessions before filtering, got %v", ov.filtered())
	}

	if _, err := tui.feedKey(Key{Kind: KeyCtrl, Byte: 14}); err != nil { // Ctrl+N
		t.Fatal(err)
	}
	vis := ov.filtered()
	if len(vis) != 1 || vis[0].id != sessionID {
		t.Fatalf("filtered = %v, want only the named session", vis)
	}
	if tui.activeOverlay != ov {
		t.Fatal("Ctrl+N must not close the session picker")
	}
}

// TestFeedKeyCtrlNStillNavigatesHistoryWithoutOverlay locks in the other half
// of the Ctrl+N gate: with no overlay open, byte 14 must still fall through
// to the pre-existing history-forward binding, unaffected by the session
// picker's namedOnly toggle.
func TestFeedKeyCtrlNStillNavigatesHistoryWithoutOverlay(t *testing.T) {
	tui := newTestTUIHelper()
	tui.history = []string{"first", "second"}
	tui.histIdx = 0
	tui.editor.setText("first")

	if _, err := tui.feedKey(Key{Kind: KeyCtrl, Byte: 14}); err != nil {
		t.Fatal(err)
	}
	if tui.histIdx != 1 {
		t.Fatalf("histIdx = %d, want 1 (Ctrl+N should navigate history forward)", tui.histIdx)
	}
}

func TestFeedKeySearchTypeDoesNotDeadlock(t *testing.T) {
	tui := newTestTUIHelper()
	tui.mu.Lock()
	tui.openSearchLocked()
	tui.mu.Unlock()

	done := make(chan struct{})
	go func() {
		_, _ = tui.feedKey(Key{Kind: KeyRune, Rune: 'x'})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("search typing deadlocked feedKey")
	}
}

// TestFeedKeyEscDoesNotCancelWhileModalOpen: with a picker overlay open, Esc
// closes the overlay first (its normal job everywhere else in the app) — it
// does not reach through to cancel the running turn. That matches Esc's role
// for every other overlay (search, btw, approval-deny): the nearest thing in
// front always gets it first.
func TestFeedKeyEscDoesNotCancelWhileModalOpen(t *testing.T) {
	tui := newTestTUIHelper()
	tui.status.Thinking = true
	tui.setActiveOverlay(newPickerOverlay("test", []pickerItem{{id: "a", label: "a"}}, "", nil))
	cancelled := make(chan struct{}, 1)
	tui.cancelMu.Lock()
	tui.cancelRun = func() { cancelled <- struct{}{} }
	tui.cancelMu.Unlock()

	_, err := tui.feedKey(Key{Kind: KeyEscape})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
		t.Fatal("Esc with a modal open should close the modal, not cancel the agent")
	case <-time.After(50 * time.Millisecond):
	}
	if tui.activeOverlay != nil {
		t.Fatal("overlay should be dismissed by Esc")
	}
}

// TestFeedKeyEscCancelsWhileRunning is the reassigned keybind: Esc (not
// Ctrl+C) stops an in-flight turn.
func TestFeedKeyEscCancelsWhileRunning(t *testing.T) {
	tui := newTestTUIHelper()
	tui.status.Thinking = true
	cancelled := make(chan struct{}, 1)
	tui.cancelMu.Lock()
	tui.cancelRun = func() { cancelled <- struct{}{} }
	tui.cancelMu.Unlock()

	_, err := tui.feedKey(Key{Kind: KeyEscape})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Esc while running should cancel immediately")
	}
	if !tui.turnCancelled {
		t.Fatal("expected turnCancelled flag")
	}
}

// TestFeedKeyCtrlCNoLongerCancelsWhileRunning locks in the other half of the
// keybind swap: Ctrl+C while running must NOT cancel the turn any more (it
// only clears typed input, or arms/confirms quit).
func TestFeedKeyCtrlCNoLongerCancelsWhileRunning(t *testing.T) {
	tui := newTestTUIHelper()
	tui.status.Thinking = true
	cancelled := make(chan struct{}, 1)
	tui.cancelMu.Lock()
	tui.cancelRun = func() { cancelled <- struct{}{} }
	tui.cancelMu.Unlock()

	_, err := tui.feedKey(Key{Kind: KeyCtrl, Byte: 3})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
		t.Fatal("Ctrl+C should no longer cancel a running turn")
	case <-time.After(50 * time.Millisecond):
	}
	if tui.turnCancelled {
		t.Fatal("turnCancelled should not be set by Ctrl+C any more")
	}
}

func TestSessionPickerResumeDoesNotDeadlock(t *testing.T) {
	st, a, sessionID := newTestStoreAndAgent(t)
	otherID := store.NewSessionID()
	st.CreateSession(&store.Session{
		ID: otherID, Cwd: "/tmp", Provider: "ollama", Model: "test",
		CreatedAt: 1, UpdatedAt: 1,
	})
	tui := newTUIWithAgent(a, sessionID)
	_, _ = tui.feedKey(Key{Kind: KeyCtrl, Byte: 19})

	done := make(chan struct{})
	go func() {
		tui.handleKeyOverlay(Key{Kind: KeyEnter})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("session resume from picker deadlocked")
	}
}
