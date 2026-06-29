package tui

import (
	"testing"
	"time"

	"poisson/internal/store"
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

func TestFeedKeyCtrlCCancelsWhileRunningWithModalOpen(t *testing.T) {
	tui := newTestTUIHelper()
	tui.status.Thinking = true
	tui.setActiveOverlay(newPickerOverlay("test", []pickerItem{{id: "a", label: "a"}}, "", nil))
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
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Ctrl+C with modal open should cancel agent, not only dismiss overlay")
	}
	if tui.activeOverlay == nil {
		t.Fatal("overlay should stay open after cancel (not dismissed)")
	}
}

func TestFeedKeyCtrlCCancelsWhileRunning(t *testing.T) {
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
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Ctrl+C while running should cancel, not wait for double-tap")
	}
	if !tui.turnCancelled {
		t.Fatal("expected turnCancelled flag")
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