package tui

import (
	"context"
	"errors"
)

// openBTW opens a floating side-question box (/btw) while the main agent keeps running.
func (t *tuiV2) openBTW(question string) {
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

func (t *tuiV2) runBTW(ctx context.Context, o *btwOverlay, question string) {
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
func (t *tuiV2) openModelPicker() {
	h := v2CmdHost{t}
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
func (t *tuiV2) openProviderPicker() {
	h := v2CmdHost{t}
	cur := t.agent.Provider().ID()
	t.setActiveOverlay(newPickerOverlay("Providers", pickerProviderItems(h), cur, func(id string) error {
		return cmdModel(h, []string{id})
	}))
}

// openSessionPicker shows recent sessions overlay.
func (t *tuiV2) openSessionPicker() {
	h := v2CmdHost{t}
	items, err := pickerSessionItems(h)
	if err != nil {
		t.scroll.appendRaw(styleError, "error listing sessions: "+err.Error())
		t.markScrollDirty()
		return
	}
	if len(items) == 0 {
		t.scroll.appendRaw(styleSystem, "no sessions")
		t.markScrollDirty()
		return
	}
	t.setActiveOverlay(newPickerOverlay("Sessions", items, t.sessionID, func(id string) error {
		return cmdResume(h, []string{id})
	}))
}

// openSearch opens in-scrollback find (Ctrl+F).
func (t *tuiV2) openSearch() {
	width := t.contentWidth()
	t.setActiveOverlay(newSearchOverlay(
		func() []ScreenRow {
			wrapped, _ := t.scroll.layoutAll(width)
			return wrapped
		},
		func(globalRow int) {
			// Caller (feed) already holds t.mu.
			wrapped, _ := t.scroll.layoutAll(width)
			max := len(wrapped) - t.scrollRows
			if max < 0 {
				max = 0
			}
			off := len(wrapped) - globalRow - t.scrollRows/2
			if off < 0 {
				off = 0
			}
			if off > max {
				off = max
			}
			t.scroll.scrollOffset = off
		},
	))
}

// hasKeyOverlay reports whether a modal that consumes keyboard input is active.
func (t *tuiV2) hasKeyOverlay() bool {
	return asKeyOverlay(t.activeOverlay) != nil
}

// blocksBackgroundInput reports whether scroll/wheel/paste should stay behind the overlay.
// Search is non-modal and allows background scroll.
func (t *tuiV2) blocksBackgroundInput() bool {
	if t.approving.Load() {
		return true
	}
	if t.activeOverlay == nil {
		return false
	}
	switch t.activeOverlay.(type) {
	case *searchOverlay, *approvalOverlay:
		return false
	}
	return true
}

func (t *tuiV2) cancelOverlayWork() {
	if b, ok := t.activeOverlay.(*btwOverlay); ok {
		b.mu.Lock()
		if c := b.cancel; c != nil {
			c()
		}
		b.mu.Unlock()
	}
}

func (t *tuiV2) setActiveOverlay(o overlay) {
	t.cancelOverlayWork()
	t.activeOverlay = o
	t.dirty.markFull()
}

func (t *tuiV2) dismissOverlay() {
	t.cancelOverlayWork()
	t.activeOverlay = nil
	t.dirty.markFull()
}

// openCommandPalette shows fuzzy command launcher (Ctrl+P).
func (t *tuiV2) openCommandPalette() {
	t.setActiveOverlay(newPaletteOverlay(func(cmd string) error {
		err := t.handleSlash(cmd)
		if errors.Is(err, errQuitSentinel) {
			t.overlayQuit.Store(true)
		}
		return err
	}))
}

func (t *tuiV2) closeOverlayAfter(prev overlay, done, cancel bool) {
	if cancel {
		t.dismissOverlay()
	} else if done && t.activeOverlay == prev {
		// onRun/onPick may replace this overlay (e.g. palette → provider picker).
		t.activeOverlay = nil
		t.dirty.markFull()
	}
}

func (t *tuiV2) handleKeyOverlay(data []byte) bool {
	ko := asKeyOverlay(t.activeOverlay)
	if ko == nil {
		return false
	}
	prev := t.activeOverlay
	handled, done, cancel := ko.feedKey(data)
	if !handled {
		return false
	}
	t.closeOverlayAfter(prev, done, cancel)
	return true
}