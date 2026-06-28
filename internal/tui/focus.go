package tui

import "time"

// focusRegion is which part of the TUI receives keyboard input.
type focusRegion uint8

const (
	focusInput focusRegion = iota
	focusConv
)

func (t *TUI) convScrollRows() int {
	if t.focusRegion == focusConv && t.scrollRows > 1 {
		return t.scrollRows - 1
	}
	return t.scrollRows
}

func (t *TUI) pinnedPromptLine(width int) string {
	idxs := t.scroll.userBlockIndices()
	if len(idxs) == 0 {
		return dim + "› (no prompts yet)" + reset
	}
	if t.convUserIdx < 0 {
		t.convUserIdx = 0
	}
	if t.convUserIdx >= len(idxs) {
		t.convUserIdx = len(idxs) - 1
	}
	text := t.scroll.userPromptText(idxs[t.convUserIdx])
	if text == "" {
		text = "(empty)"
	}
	label := truncatePlain(text, width-6)
	return fgYellow + bold + "› " + reset + fgCyan + label + reset
}

func (t *TUI) enterConvFocus() {
	if !t.scroll.hasUserBlocks() {
		return
	}
	t.focusRegion = focusConv
	idxs := t.scroll.userBlockIndices()
	t.convUserIdx = len(idxs) - 1
	t.scrollToConvPrompt()
	t.markScrollDirty()
	t.markInputDirty()
}

func (t *TUI) scrollToConvPrompt() {
	idxs := t.scroll.userBlockIndices()
	if len(idxs) == 0 {
		return
	}
	if t.convUserIdx < 0 {
		t.convUserIdx = 0
	}
	if t.convUserIdx >= len(idxs) {
		t.convUserIdx = len(idxs) - 1
	}
	t.scroll.scrollBlockToTop(idxs[t.convUserIdx], t.convScrollRows(), t.contentWidth())
}

func (t *TUI) stepConvPrompt(dir int) {
	idxs := t.scroll.userBlockIndices()
	if len(idxs) == 0 {
		return
	}
	t.convUserIdx += dir
	if t.convUserIdx < 0 {
		t.convUserIdx = 0
	}
	if t.convUserIdx >= len(idxs) {
		t.convUserIdx = len(idxs) - 1
	}
	t.scrollToConvPrompt()
	t.markScrollDirty()
}

func (t *TUI) handleTabKey() {
	if t.focusRegion == focusConv {
		t.focusRegion = focusInput
		t.markScrollDirty()
		t.markInputDirty()
		return
	}
	if t.handleTab() {
		t.markInputDirty()
		return
	}
	if t.completion != nil && !t.completion.empty() {
		return
	}
	t.enterConvFocus()
}

func (t *TUI) feedConvFocus(k Key) (handled bool) {
	if k.Kind == KeyTab {
		t.handleTabKey()
		return true
	}
	if delta, ok := scrollDeltaForKey(k, t.convScrollRows()); ok {
		t.scrollByDelta(delta)
		return true
	}
	switch k.Kind {
	case KeyArrowUp:
		t.scrollByDelta(1)
		return true
	case KeyArrowDown:
		t.scrollByDelta(-1)
		return true
	case KeyShiftArrowLeft:
		t.stepConvPrompt(-1)
		return true
	case KeyShiftArrowRight:
		t.stepConvPrompt(1)
		return true
	}
	return false
}

func (t *TUI) scrollViewportRows() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.convScrollRows()
}

// trimScrollbackFromLastUserLocked removes the last user turn from scrollback.
// Caller must hold t.mu.
func (t *TUI) trimScrollbackFromLastUserLocked() {
	idxs := t.scroll.userBlockIndices()
	if len(idxs) == 0 {
		return
	}
	t.scroll.trimFromBlockIndex(idxs[len(idxs)-1])
	t.markScrollDirty()
}

// trimScrollbackFromLastUser removes the last user turn from on-screen scrollback.
func (t *TUI) trimScrollbackFromLastUser() {
	t.mu.Lock()
	t.trimScrollbackFromLastUserLocked()
	t.mu.Unlock()
}

// resetSessionViewLocked clears scrollback and focus after switching sessions.
// Caller must hold t.mu.
func (t *TUI) resetSessionViewLocked() {
	t.scroll = newScrollback(8192)
	t.focusRegion = focusInput
	t.convUserIdx = 0
	t.activeOverlay = nil
	t.completion = nil
	t.hydrateScrollbackLocked()
	t.dirty.markFull()
}

// resetSessionView clears scrollback and focus state after switching sessions,
// then replays stored messages so the UI matches the active session.
func (t *TUI) resetSessionView() {
	t.mu.Lock()
	t.resetSessionViewLocked()
	t.mu.Unlock()
}

func (t *TUI) setEphemeralHintLocked(msg string, d time.Duration) {
	t.status.Hint = msg
	t.hintExpiry = time.Now().Add(d)
	t.dirty.markStatus()
}

func (t *TUI) setEphemeralHint(msg string, d time.Duration) {
	t.mu.Lock()
	t.setEphemeralHintLocked(msg, d)
	t.mu.Unlock()
}

func (t *TUI) maybeClearHint() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.status.Hint == "" || t.hintExpiry.IsZero() {
		return
	}
	if time.Now().After(t.hintExpiry) {
		t.status.Hint = ""
		t.hintExpiry = time.Time{}
		t.dirty.markStatus()
	}
}

func (t *TUI) cancelActiveRunLocked() {
	t.cancelMu.Lock()
	cancel := t.cancelRun
	t.cancelMu.Unlock()
	if cancel != nil {
		cancel()
	}
	t.setEphemeralHintLocked("cancelled — Ctrl+C again to exit", 4*time.Second)
}

func (t *TUI) cancelActiveRun() {
	t.mu.Lock()
	t.cancelActiveRunLocked()
	t.mu.Unlock()
}

func (t *TUI) flashApprovalHint() {
	t.setEphemeralHint("approval: A/y/Enter allow · D/n/Esc deny · ↑↓ scroll · Ctrl+C cancel", 3*time.Second)
}

func containsTab(data []byte) bool {
	for _, b := range data {
		if b == 9 {
			return true
		}
	}
	return false
}