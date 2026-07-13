package tui

import "testing"

// TestPaletteNamePrefillsInputInsteadOfExecuting is a regression test: picking
// /name from the Ctrl+P command palette used to run "/name" with no argument
// immediately, which just prints "title: (unset)" — a confusing no-op for
// what's actually meant to be a rename action. It should instead prefill the
// input with "/name " and close the palette, letting the user type the title.
func TestPaletteNamePrefillsInputInsteadOfExecuting(t *testing.T) {
	e := newTUIIntegEnv(t, nil)
	e.tui.mu.Lock()
	e.tui.openCommandPalette()
	p, ok := e.tui.activeOverlay.(*paletteOverlay)
	if !ok {
		e.tui.mu.Unlock()
		t.Fatal("palette overlay not active")
	}
	p.filter = "/name"
	done, _, _ := p.feedKey(Key{Kind: KeyEnter})
	e.tui.closeOverlayAfter(p, done, false)
	text := e.tui.editor.text()
	stillOpen := e.tui.activeOverlay != nil
	e.tui.mu.Unlock()

	if text != "/name " {
		t.Errorf("editor text = %q, want %q (prefilled, not executed)", text, "/name ")
	}
	if stillOpen {
		t.Error("palette should close after picking /name")
	}

	// The title must be unchanged — /name must not have actually run.
	sess, err := e.store.GetSession(e.sid)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.Title != nil {
		t.Errorf("session title = %v, want nil (unset) — /name should not have executed", *sess.Title)
	}
}
