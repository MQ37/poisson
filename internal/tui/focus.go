package tui

import (
	"strings"
	"time"
)

// focusRegion is which part of the TUI receives keyboard input.
type focusRegion uint8

const (
	focusInput focusRegion = iota
	focusConv
)

// convPinRows is the number of scroll-region rows reserved for the conv-focus
// turn header band (full-width background, not part of scrollback content).
func (t *TUI) convPinRows() int {
	n := t.scroll.runningSubagentCount()
	if t.focusRegion == focusConv {
		n++
	}
	return n
}

func (t *TUI) convScrollRows() int {
	pin := t.convPinRows()
	if t.scrollRows > pin {
		return t.scrollRows - pin
	}
	return t.scrollRows
}

// offsetConvDirtyRows shifts content dirty indices below the pin band and always
// includes the pin row(s) so the header repaints on every partial scroll update.
func (t *TUI) offsetConvDirtyRows(rows []int) []int {
	pin := t.convPinRows()
	if pin == 0 || len(rows) == 0 {
		return rows
	}
	out := make([]int, 0, pin+len(rows))
	for i := 0; i < pin; i++ {
		out = append(out, i)
	}
	for _, r := range rows {
		out = append(out, r+pin)
	}
	return out
}

func convHeaderTurnStyle() string {
	if themeName == "light" {
		return bold + fgBlue
	}
	return bold + fgYellow
}

func convHeaderPromptStyle() string {
	if themeName == "light" {
		return bold + fgBlack
	}
	// Bright white reads clearly on the dark blue band; fgBlack/fgCyan do not.
	return bold + "\x1b[97m"
}

func (t *TUI) pinnedPromptLine(width int) string {
	if width < 1 {
		width = 1
	}
	idxs := t.scroll.userBlockIndices()
	if len(idxs) == 0 {
		return fillWidthBG(bgBlue, convHeaderPromptStyle()+"(no prompts yet)", width)
	}
	idx := t.convUserIdx
	if idx < 0 {
		idx = 0
	}
	if idx >= len(idxs) {
		idx = len(idxs) - 1
	}
	text := t.scroll.userPromptText(idxs[idx])
	if text == "" {
		text = "(empty)"
	}
	turn := "turn " + itoa(idx+1) + "/" + itoa(len(idxs))
	avail := width - visibleWidth(turn) - 2
	if avail < 1 {
		avail = 1
	}
	prompt := truncatePlain(text, avail)
	body := convHeaderTurnStyle() + turn + "  " + convHeaderPromptStyle() + prompt
	return fillWidthBG(bgBlue, body, width)
}

// fillWidthBG paints content on a full-width background band (pads with spaces).
func fillWidthBG(bg, content string, width int) string {
	pad := width - visibleWidth(content)
	if pad < 0 {
		content = truncateToWidth(content, width)
		pad = width - visibleWidth(content)
	}
	if pad < 0 {
		pad = 0
	}
	// Re-apply bg before padding: embedded resets must not leave the tail uncolored.
	return bg + content + bg + strings.Repeat(" ", pad) + reset
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
	t.scroll.scrollBlockToTop(idxs[t.convUserIdx], t.convScrollRows(), t.contentWidth(), 1)
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
	if k.Meta && (k.Kind == KeyBackspace || k.Kind == KeyArrowLeft || k.Kind == KeyArrowRight) {
		return false
	}
	if k.Kind == KeyTab {
		t.handleTabKey()
		return true
	}
	w := t.contentWidth()
	if t.scroll.focusedToolExpanded(w) {
		switch k.Kind {
		case KeyArrowUp:
			if t.scroll.scrollFocusedTool(w, -1) {
				t.markScrollDirty()
			}
			return true
		case KeyArrowDown:
			if t.scroll.scrollFocusedTool(w, 1) {
				t.markScrollDirty()
			}
			return true
		}
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

// resetSessionViewLocked clears scrollback and focus after switching sessions.
// Caller must hold t.mu.
func (t *TUI) resetSessionViewLocked() {
	t.clearScrollbackKeepIntroLocked()
	t.focusRegion = focusInput
	t.convUserIdx = 0
	t.activeOverlay = nil
	t.completion = nil
	t.hydrateScrollbackLocked()
	t.syncHeaderFromAgentLocked()
	t.dirty.markFull()
	// /resume can switch to a session on a different provider — same reasoning
	// as refreshHostHeader in commands.go.
	t.triggerUsageRefreshLocked()
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
		t.turnCancelled = true
		t.exitArmed = true
		cancel()
	}
	// Cancelling abandons any messages queued behind this turn — the user hit
	// stop, so nothing pending should be sent.
	if len(t.queued) > 0 {
		t.queued = nil
		t.dirty.markFull()
	}
	t.setEphemeralHintLocked("cancelled — Ctrl+C again to exit", 2*time.Second)
}

func (t *TUI) clearCompletionLocked() {
	t.completion = nil
	t.lastCompletionRows = 0
}

func (t *TUI) flashCompletionHintLocked() {
	t.setEphemeralHintLocked("close completion first (Esc)", 2*time.Second)
}

func (t *TUI) cancelActiveRun() {
	t.mu.Lock()
	t.cancelActiveRunLocked()
	t.mu.Unlock()
}

func (t *TUI) flashApprovalHint() {
	t.setEphemeralHint("approval: A/y/Enter allow · D/n/Esc deny · ↑↓ scroll · Ctrl+C cancel", 3*time.Second)
}

func (t *TUI) flashOverlayHintLocked() {
	t.setEphemeralHintLocked("close overlay first (Esc)", 2*time.Second)
}

// prepareShutdownLocked cancels overlays and any in-flight agent turn before exit.
// Caller must hold t.mu.
func (t *TUI) prepareShutdownLocked() {
	t.cancelOverlayWork()
	t.activeOverlay = nil
	t.cancelMu.Lock()
	cancel := t.cancelRun
	t.cancelMu.Unlock()
	if cancel != nil {
		t.turnCancelled = true
		cancel()
	}
}

// waitForAgentStop blocks briefly until the agent goroutine finishes after shutdown.
func (t *TUI) waitForAgentStop() {
	t.mu.Lock()
	t.prepareShutdownLocked()
	t.mu.Unlock()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		t.mu.Lock()
		busy := t.status.Thinking || t.activeTools > 0
		t.mu.Unlock()
		if !busy {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// syncConvUserIdxFromScrollLocked updates the pinned turn from the viewport top.
// Caller must hold t.mu.
func (t *TUI) syncConvUserIdxFromScrollLocked() {
	if t.focusRegion != focusConv {
		return
	}
	idxs := t.scroll.userBlockIndices()
	if len(idxs) == 0 {
		return
	}
	w := t.contentWidth()
	viewH := t.convScrollRows()
	_, start, _ := t.scroll.viewportRange(viewH, w)
	chosen := 0
	for i, bi := range idxs {
		if t.scroll.blockGlobalStart(bi, w) <= start {
			chosen = i
		}
	}
	if chosen != t.convUserIdx {
		t.convUserIdx = chosen
		t.dirty.markScrollRows(0)
	}
}
