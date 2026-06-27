package tui

import (
	"context"
)

// openBTW opens a floating side-question box (/btw) while the main agent keeps running.
func (t *tuiV2) openBTW(question string) {
	if t.activeOverlay != nil {
		if b, ok := t.activeOverlay.(*btwOverlay); ok {
			b.mu.Lock()
			if c := b.cancel; c != nil {
				c()
			}
			b.mu.Unlock()
		}
	}
	maxH := t.rows * 15 / 100
	if maxH < 3 {
		maxH = 3
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
	t.activeOverlay = newPickerOverlay("Models ("+prov+")", items, t.agent.Model(), func(id string) error {
		return cmdModel(h, []string{prov + "/" + id})
	})
	t.dirty.markFull()
}

// openProviderPicker shows provider switcher overlay.
func (t *tuiV2) openProviderPicker() {
	h := v2CmdHost{t}
	cur := t.agent.Provider().ID()
	t.activeOverlay = newPickerOverlay("Providers", pickerProviderItems(h), cur, func(id string) error {
		return cmdModel(h, []string{id})
	})
	t.dirty.markFull()
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
	t.activeOverlay = newPickerOverlay("Sessions", items, t.sessionID, func(id string) error {
		return cmdResume(h, []string{id})
	})
	t.dirty.markFull()
}

// openSearch opens in-scrollback find (Ctrl+F).
func (t *tuiV2) openSearch() {
	width := t.contentWidth()
	t.activeOverlay = newSearchOverlay(
		func() []ScreenRow {
			wrapped, _ := t.scroll.layoutAll(width)
			return wrapped
		},
		func(globalRow int) {
			t.mu.Lock()
			defer t.mu.Unlock()
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
	)
	t.dirty.markFull()
}

// hasKeyOverlay reports whether a modal that consumes keyboard input is active.
func (t *tuiV2) hasKeyOverlay() bool {
	return asKeyOverlay(t.activeOverlay) != nil
}

// openCommandPalette shows fuzzy command launcher (Ctrl+P).
func (t *tuiV2) openCommandPalette() {
	t.activeOverlay = newPaletteOverlay(func(cmd string) error {
		return t.handleSlash(cmd)
	})
	t.dirty.markFull()
}

func (t *tuiV2) closeOverlayAfter(prev overlay, done, cancel bool) {
	if cancel {
		t.activeOverlay = nil
	} else if done && t.activeOverlay == prev {
		// onRun/onPick may replace this overlay (e.g. palette → provider picker).
		t.activeOverlay = nil
	}
	t.dirty.markFull()
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