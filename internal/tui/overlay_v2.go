package tui

import (
	"context"
	"errors"
	"time"
)

// openBTW opens a floating side-question box (/btw) while the main agent keeps running.
func (t *TUI) openBTW(question string) {
	t.cancelOverlayWork()
	o := newBTWOverlay(question)
	ctx, cancel := context.WithCancel(context.Background())
	o.setCancel(cancel)
	t.activeOverlay = o
	t.currentBTW = o
	t.dirty.markFull()
	go t.runBTW(ctx, o, question)
}

// openBTWPrompt opens the Ctrl+B floating input for a /btw side question —
// same flow as typing "/btw <question>" in the main input, but usable
// without clearing whatever the user already has drafted there (including
// while a turn is running, same as /btw itself).
func (t *TUI) openBTWPrompt() {
	t.setActiveOverlay(newBTWPromptOverlay(func(question string) {
		t.openBTW(question)
	}))
}

func (t *TUI) runBTW(ctx context.Context, o *btwOverlay, question string) {
	onStatus := func(text string) {
		o.setStatus(text)
		t.mu.Lock()
		t.dirty.markFull()
		t.mu.Unlock()
	}
	textCh, errCh, err := t.agent.StreamQuickAnswer(ctx, question, onStatus)
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
	if t.sessionBusyLocked() {
		t.setEphemeralHintLocked("cannot change model while agent is running or compacting", 3*time.Second)
		return
	}
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
	if t.sessionBusyLocked() {
		t.setEphemeralHintLocked("cannot change provider while agent is running or compacting", 3*time.Second)
		return
	}
	h := tuiCmdHost{t}
	cur := t.agent.Provider().ID()
	t.setActiveOverlay(newPickerOverlay("Providers", pickerProviderItems(h), cur, func(id string) error {
		return cmdModel(h, []string{id})
	}))
}

// openClassifierModelPicker shows the bash-risk classifier model picker
// (/classifier-model with no argument). Unlike the model picker this never
// touches the session's own model, so it stays usable while a turn runs.
func (t *TUI) openClassifierModelPicker() {
	h := tuiCmdHost{t}
	items, err := pickerClassifierModelItems(h)
	if err != nil {
		t.scroll.appendRaw(styleError, "error listing models: "+err.Error())
		t.markScrollDirty()
		return
	}
	cur := t.agent.ClassifierModel()
	if !t.agent.ClassifierModelPinned() {
		cur = "default"
	}
	title := "Bash-risk classifier model (" + t.agent.Provider().ID() + ")"
	t.setActiveOverlay(newPickerOverlay(title, items, cur, func(id string) error {
		return cmdClassifierModel(h, []string{id})
	}))
}

// openEffortPicker shows thinking-effort level picker (Ctrl+L).
func (t *TUI) openEffortPicker() {
	if t.sessionBusyLocked() {
		t.setEphemeralHintLocked("cannot change effort while agent is running or compacting", 3*time.Second)
		return
	}
	h := tuiCmdHost{t}
	cur := t.agent.Effort()
	t.setActiveOverlay(newPickerOverlay("Effort", pickerEffortItems(h), cur, func(id string) error {
		if err := cmdEffort(h, []string{id}); err != nil {
			return err
		}
		t.status.Effort = id
		t.dirty.markStatus()
		return nil
	}))
}

// openSessionPicker shows recent sessions overlay.
func (t *TUI) openSessionPicker() {
	if t.sessionBusyLocked() {
		t.setEphemeralHintLocked("cannot switch session while agent is running or compacting", 3*time.Second)
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
	ov := newPickerOverlay("Sessions", items, t.sessionID, func(id string) error {
		return cmdResume(h, []string{id})
	})
	// Ctrl+D in the session picker deletes the selected session (after an Enter
	// confirmation). The active session is guarded against deletion in the overlay.
	ov.onDelete = func(id string) error { return t.agent.Store().DeleteSession(id) }
	ov.namedFilterEnabled = true
	ov.footerHint = "↑↓ move · Enter row · Ctrl+D del · Ctrl+N named · Esc · Ctrl+C"
	t.setActiveOverlay(ov)
}

// openSearchLocked opens search. Caller must hold t.mu (e.g. feedKey, handleSlash).
func (t *TUI) openSearchLocked() {
	t.searchHadConvFocus = t.focusRegion == focusConv
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
		func() bool { return t.running() },
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

// cancelOverlayWork is the one place that ever tears a live /btw stream
// down — cancels its context, clears currentBTW, and signals closedCh so a
// parked non-btw approval (see tui.TUI.Approve) knows it can show its own
// prompt now. Checks currentBTW rather than activeOverlay's current type:
// the panel being torn down might not even be the active overlay right now
// (e.g. its own approval prompt is what's showing), but it's still the live
// /btw session that needs tearing down.
func (t *TUI) cancelOverlayWork() {
	b := t.currentBTW
	if b == nil {
		return
	}
	b.mu.Lock()
	if c := b.cancel; c != nil {
		c()
	}
	b.mu.Unlock()
	t.currentBTW = nil
	b.markClosed()
}

func (t *TUI) setActiveOverlay(o overlay) {
	if t.activeOverlay != nil {
		t.setEphemeralHintLocked("replaced open overlay", 2*time.Second)
	}
	t.clearCompletionLocked()
	t.cancelOverlayWork()
	t.activeOverlay = o
	t.dirty.markFull()
}

func (t *TUI) dismissOverlay() {
	wasSearch := false
	if _, ok := t.activeOverlay.(*searchOverlay); ok {
		wasSearch = true
	}
	t.cancelOverlayWork()
	t.activeOverlay = nil
	if wasSearch && t.searchHadConvFocus {
		t.focusRegion = focusConv
		t.searchHadConvFocus = false
	}
	t.dirty.markFull()
}

// openCommandPalette shows fuzzy command launcher (Ctrl+P).
func (t *TUI) openCommandPalette() {
	t.setActiveOverlay(newPaletteOverlay(func(cmd string) error {
		// /name with no argument just prints the current title (or "(unset)"),
		// which is a nonsensical outcome for what's actually a rename action —
		// prefill the input with the command instead of auto-running it, so the
		// user types the title before submitting. Caller (handleKeyOverlay) already
		// holds t.mu.
		if cmd == "/name" {
			t.editor.setText("/name ")
			t.dirty.markFull()
			return nil
		}
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

// overlayPinOffset is extra scroll rows reserved for the conv turn header band.
func (t *TUI) overlayPinOffset() int {
	return t.convPinRows()
}

func (t *TUI) handleOverlayPaste(k Key) bool {
	switch o := t.activeOverlay.(type) {
	case *searchOverlay:
		return o.appendPaste(k.Text)
	case *filterableListOverlay:
		return appendOverlayFilterText(&o.filter, k.Text, &o.idx)
	case *btwPromptOverlay:
		return appendOverlayFilterText(&o.query, k.Text, nil)
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
