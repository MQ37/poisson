package tui

import (
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/provider"
)

func TestBTWPromptOverlayRenderShowsPlaceholderThenQuery(t *testing.T) {
	o := newBTWPromptOverlay(nil)
	_, lines := o.render(1, 80)
	if !strings.Contains(stripANSI(lines[0]), "btw:") {
		t.Fatalf("expected 'btw:' label, got %q", stripANSI(lines[0]))
	}
	if !strings.Contains(stripANSI(lines[0]), "draft stays put") {
		t.Fatalf("expected placeholder hint, got %q", stripANSI(lines[0]))
	}

	o.query = "what year is it"
	_, lines = o.render(1, 80)
	if !strings.Contains(stripANSI(lines[0]), "what year is it") {
		t.Fatalf("expected typed query in render, got %q", stripANSI(lines[0]))
	}
}

func TestBTWPromptOverlayTypingAndBackspace(t *testing.T) {
	o := newBTWPromptOverlay(nil)
	for _, r := range "hi" {
		handled, done, cancel := o.feedKey(Key{Kind: KeyRune, Rune: r})
		if !handled || done || cancel {
			t.Fatalf("typing %q: handled=%v done=%v cancel=%v", r, handled, done, cancel)
		}
	}
	if o.query != "hi" {
		t.Fatalf("query = %q, want %q", o.query, "hi")
	}
	handled, done, cancel := o.feedKey(Key{Kind: KeyBackspace})
	if !handled || done || cancel {
		t.Fatalf("backspace: handled=%v done=%v cancel=%v", handled, done, cancel)
	}
	if o.query != "h" {
		t.Fatalf("query after backspace = %q, want %q", o.query, "h")
	}
}

func TestBTWPromptOverlayEnterWithEmptyQueryStaysOpen(t *testing.T) {
	called := false
	o := newBTWPromptOverlay(func(string) { called = true })
	handled, done, cancel := o.feedKey(Key{Kind: KeyEnter})
	if !handled || done || cancel {
		t.Fatalf("enter on empty query: handled=%v done=%v cancel=%v", handled, done, cancel)
	}
	if called {
		t.Fatal("onSubmit should not fire for an empty question")
	}
}

func TestBTWPromptOverlayEnterSubmitsAndCloses(t *testing.T) {
	var got string
	o := newBTWPromptOverlay(func(q string) { got = q })
	o.query = "  what is 2+2  "
	handled, done, cancel := o.feedKey(Key{Kind: KeyEnter})
	if !handled || !done || cancel {
		t.Fatalf("enter with question: handled=%v done=%v cancel=%v", handled, done, cancel)
	}
	if got != "what is 2+2" {
		t.Fatalf("onSubmit question = %q, want trimmed %q", got, "what is 2+2")
	}
}

func TestBTWPromptOverlayEscCancels(t *testing.T) {
	called := false
	o := newBTWPromptOverlay(func(string) { called = true })
	o.query = "unsent"
	handled, done, cancel := o.feedKey(Key{Kind: KeyEscape})
	if !handled || !done || !cancel {
		t.Fatalf("esc: handled=%v done=%v cancel=%v", handled, done, cancel)
	}
	if called {
		t.Fatal("Esc must not submit the draft question")
	}
}

// TestFeedKeyCtrlBOpensPromptAndKeepsDraft: Ctrl+B must open the popup
// without touching whatever the user already has typed in the main input —
// the whole point of the feature over clearing the line to type "/btw".
func TestFeedKeyCtrlBOpensPromptAndKeepsDraft(t *testing.T) {
	tui := newTestTUIHelper()
	tui.editor.setText("half-written prompt, don't lose me")

	_, err := tui.feedKey(Key{Kind: KeyCtrl, Byte: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := tui.activeOverlay.(*btwPromptOverlay); !ok {
		t.Fatalf("expected btwPromptOverlay active, got %T", tui.activeOverlay)
	}
	if got := tui.editor.text(); got != "half-written prompt, don't lose me" {
		t.Fatalf("main input text changed: %q", got)
	}
}

// TestFeedKeyCtrlBDispatchesWhileBusy mirrors TestQueue_BTWDispatchesWhileBusy:
// the popup must be reachable even mid-turn, same as /btw itself.
func TestFeedKeyCtrlBDispatchesWhileBusy(t *testing.T) {
	tui := newTestTUIHelper()
	tui.status.Thinking = true

	_, err := tui.feedKey(Key{Kind: KeyCtrl, Byte: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := tui.activeOverlay.(*btwPromptOverlay); !ok {
		t.Fatalf("expected btwPromptOverlay active while busy, got %T", tui.activeOverlay)
	}
}

// TestTUIInteg_CtrlBPromptSubmitRunsSameFlowAsSlashBTW drives the popup
// end-to-end: typing a question and hitting Enter must hand off to the exact
// same side-question flow /btw uses (real Agent + provider round trip),
// while the draft already sitting in the main input survives untouched.
func TestTUIInteg_CtrlBPromptSubmitRunsSameFlowAsSlashBTW(t *testing.T) {
	e := newTUIIntegEnv(t, nil)
	e.prov.SetResponses([][]provider.StreamEvent{
		provider.FakeTextResponse("4", nil),
	})

	e.tui.mu.Lock()
	e.tui.editor.setText("unrelated draft the user was typing")
	e.tui.mu.Unlock()

	if _, err := e.tui.feedKey(Key{Kind: KeyCtrl, Byte: 2}); err != nil {
		t.Fatalf("Ctrl+B: %v", err)
	}
	for _, r := range "what is 2+2" {
		if _, err := e.tui.feedKey(Key{Kind: KeyRune, Rune: r}); err != nil {
			t.Fatalf("typing: %v", err)
		}
	}
	if _, err := e.tui.feedKey(Key{Kind: KeyEnter}); err != nil {
		t.Fatalf("enter: %v", err)
	}

	e.tui.mu.Lock()
	_, isBTW := e.tui.activeOverlay.(*btwOverlay)
	draft := e.tui.editor.text()
	e.tui.mu.Unlock()

	if !isBTW {
		t.Fatal("expected the popup to hand off into the btw answer overlay")
	}
	if draft != "unrelated draft the user was typing" {
		t.Fatalf("main input draft was lost: %q", draft)
	}

	waitFor(t, func() bool {
		e.tui.mu.Lock()
		defer e.tui.mu.Unlock()
		bo, ok := e.tui.activeOverlay.(*btwOverlay)
		if !ok {
			return false
		}
		_, _, _, processing, _, _ := bo.snapshot()
		return !processing
	})

	screen := e.render()
	if !strings.Contains(screen, "4") {
		t.Errorf("rendered screen missing the btw answer, got:\n%s", screen)
	}
}
