package tui

import (
	"context"
	"errors"
	"time"
)

// openBTW opens a floating side-question box (/btw) while the main agent keeps running.
func (t *TUI) openBTW(question string) {
	t.cancelOverlayWork()
	maxH := t.scrollRows * 2 / 5
	if maxH < 10 {
		maxH = 10
	}
	if maxH > t.scrollRows-2 {
		maxH = t.scrollRows - 2
	}
	if maxH < 5 {
		maxH = 5
	}
	o := newBTWOverlay(question, maxH)
	ctx, cancel := context.WithCancel(context.Background())
	o.setCancel(cancel)
	t.activeOverlay = o
	t.dirty.markFull()
	go t.runBTW(ctx, o, question)
}

func (t *TUI) runBTW(ctx context.Context, o *btwOverlay, question string) {
	textCh, errCh, err := t.agent.StreamQuickAnswer(ctx, question)
	if err != nil {
		t.mu.Lock()
		o.finish(err)
		t.dirty.markFull()
		t.mu.Unlock()
		return
	}
	for chunk := range textCh {
		t.mu.Lock()
		o.appendText(chunk)
		t.dirty.markFull()
		t.mu.Unlock()
	}
	var streamErr error
	select {
	case streamErr = <-errCh:
	default:
	}
	t.mu.Lock()
	o.finish(streamErr)
	t.dirty.markFull()
	t.mu.Unlock()
}

// openModelPicker shows an interactive model picker overlay.
func (t *TUI) openModelPicker() {
	h := tuiCmdHost{t}
	items, err := pickerModelItems(h)
	if err != nil {
		t.scroll.appendRaw(styleError, "error listing models: "+err.Error())
		t.markScrollDirty()
		return
	}
	prov := t.agent.Provider().ID()
	t.setActiveOverlay(newPickerOverlay("Models ("+prov+")", items, t.agent.Model(), func(id string) error {
		return cmdModel(h, []string{prov + "/" + id})
	}))
}

// openProviderPicker shows provider switcher overlay.
func (t *TUI) openProviderPicker() {
	h := tuiCmdHost{t}
	cur := t.agent.Provider().ID()
	t.setActiveOverlay(newPickerOverlay("Providers", pickerProviderItems(h), cur, func(id string) error {
		return cmdModel(h, []string{id})
	}))
}

// openSessionPicker shows recent sessions overlay.
func (t *TUI) openSessionPicker() {
	if t.running() {
		t.setEphemeralHintLocked("cannot switch session while agent is running", 3*time.Second)
		return
	}
	h := tuiCmdHost{t}
	items, err := pickerSessionItems(h)
	if err != nil {
		t.setActiveOverlay(newPickerOverlay("Sessions", []pickerItem{
			{id: "", label: "error: " + err.Error()},
		}, "", func(string) error { return nil }))
		return
	}
	if len(items) == 0 {
		t.setActiveOverlay(newPickerOverlay("Sessions", []pickerItem{
			{id: "", label: "(no sessions — use /new)"},
		}, "", func(string) error { return nil }))
		return
	}
	t.setActiveOverlay(newPickerOverlay("Sessions", items, t.sessionID, func(id string) error {
		return cmdResume(h, []string{id})
	}))
}

// openSearch opens in-scrollback find (Ctrl+F).
func (t *TUI) openSearch() {
	t.mu.Lock()
	t.openSearchLocked()
	t.mu.Unlock()
}

// openSearchLocked opens search. Caller must hold t.mu (e.g. feedKey, handleSlash).
func (t *TUI) openSearchLocked() {
	t.focusRegion = focusInput
	t.setActiveOverlay(newSearchOverlay(
		func() []ScreenRow {
			width := t.contentWidth()
			wrapped, _ := t.scroll.layoutAll(width)
			return wrapped
		},
		func(globalRow int) {
			t.scrollToSearchMatchLocked(globalRow)
		},
	))
}

// scrollToSearchMatchLocked scrolls so globalRow is near the viewport center.
// Caller must hold t.mu.
func (t *TUI) scrollToSearchMatchLocked(globalRow int) {
	width := t.contentWidth()
	viewH := t.convScrollRows()
	wrapped, _ := t.scroll.layoutAll(width)
	max := len(wrapped) - viewH
	if max < 0 {
		max = 0
	}
	off := len(wrapped) - globalRow - viewH/2
	if off < 0 {
		off = 0
	}
	if off > max {
		off = max
	}
	t.scroll.scrollOffset = off
}

// hasKeyOverlay reports whether a modal that consumes keyboard input is active.
func (t *TUI) hasKeyOverlay() bool {
	return asKeyOverlay(t.activeOverlay) != nil
}

// blocksBackgroundInput reports whether scroll/wheel/paste should stay behind the overlay.
// Search is non-modal and allows background scroll.
func (t *TUI) blocksBackgroundInput() bool {
	if t.approving.Load() {
		return true
	}
	if t.activeOverlay == nil {
		return false
	}
	switch t.activeOverlay.(type) {
	case *searchOverlay:
		return false
	}
	return true
}

func (t *TUI) cancelOverlayWork() {
	if b, ok := t.activeOverlay.(*btwOverlay); ok {
		b.mu.Lock()
		if c := b.cancel; c != nil {
			c()
		}
		b.mu.Unlock()
	}
}

func (t *TUI) setActiveOverlay(o overlay) {
	t.cancelOverlayWork()
	t.activeOverlay = o
	t.dirty.markFull()
}

func (t *TUI) dismissOverlay() {
	t.cancelOverlayWork()
	t.activeOverlay = nil
	t.dirty.markFull()
}

// openCommandPalette shows fuzzy command launcher (Ctrl+P).
func (t *TUI) openCommandPalette() {
	t.setActiveOverlay(newPaletteOverlay(func(cmd string) error {
		err := t.handleSlash(cmd)
		if errors.Is(err, errQuitSentinel) {
			t.overlayQuit.Store(true)
		}
		return err
	}))
}

func (t *TUI) closeOverlayAfter(prev overlay, done, cancel bool) {
	if cancel {
		t.dismissOverlay()
	} else if done && t.activeOverlay == prev {
		// onRun/onPick may replace this overlay (e.g. palette → provider picker).
		t.activeOverlay = nil
		t.dirty.markFull()
	}
}

// overlayPinOffset is extra scroll rows reserved for the pinned prompt in conv focus.
func (t *TUI) overlayPinOffset() int {
	if t.focusRegion == focusConv {
		return 1
	}
	return 0
}

func (t *TUI) handleOverlayPaste(k Key) bool {
	switch o := t.activeOverlay.(type) {
	case *searchOverlay:
		return o.appendPaste(k.Text)
	case *filterableListOverlay:
		return appendOverlayFilterText(&o.filter, k.Text, &o.idx)
	}
	return false
}

func (t *TUI) handleKeyOverlay(k Key) bool {
	ko := asKeyOverlay(t.activeOverlay)
	if ko == nil {
		return false
	}
	prev := t.activeOverlay
	handled, done, cancel := ko.feedKey(k)
	if !handled {
		return false
	}
	if _, ok := t.activeOverlay.(*searchOverlay); ok {
		t.markScrollDirty()
	} else if !done && !cancel {
		// Full repaint so boxed modal rows (selection marker, filter) update reliably.
		t.dirty.markFull()
	} else {
		t.dirty.markOverlay()
	}
	t.closeOverlayAfter(prev, done, cancel)
	return true
}