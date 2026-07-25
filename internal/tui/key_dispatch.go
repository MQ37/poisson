package tui

import (
	"errors"
	"fmt"
	"time"

	"github.com/mq37/poisson/internal/agent"
)

func (t *TUI) feed(data []byte) (bool, error) {
	for _, k := range t.keyDec.Push(data) {
		if quit, err := t.feedKey(k); quit || err != nil {
			return quit, err
		}
	}
	return false, nil
}

// approvalRoutesToHandler reports whether a key pressed while a bash approval is
// pending should flow to the normal handler (feedKey) rather than being treated
// as an approval answer. This lets the user Tab into conversation focus,
// scroll the conversation, and expand/collapse thinking or tool-card blocks
// while the approval stays pending. Answer keys (a/y/d/n/Enter/Esc in input
// focus) and command-panel arrows are handled by the caller.
func approvalRoutesToHandler(k Key, convFocus bool, scrollRows int) bool {
	if k.Kind == KeyTab || k.Kind == KeyShiftTab {
		return true
	}
	if _, ok := scrollDeltaForKey(k, scrollRows); ok {
		return true
	}
	// Ctrl+T (toggle thinking) and Ctrl+E (expand/collapse tool card) are
	// pure display toggles, harmless regardless of focus — neither collides
	// with an approval answer key (keyApprovalAnswer only recognizes
	// Escape/Enter/a/y/d/n), so route them unconditionally like scroll keys
	// instead of swallowing them into "not a valid answer" while a prompt
	// is pending.
	if k.Kind == KeyCtrl && (k.Byte == 5 || k.Byte == 20) {
		return true
	}
	if convFocus && (k.isNavUp() || k.isNavDown() || k.Kind == KeyEscape) {
		return true
	}
	return false
}

func (t *TUI) searchBlocksEditorKey(k Key) bool {
	switch k.Kind {
	case KeyTab, KeyEnter:
		return true
	case KeyShiftEnter:
		return true
	case KeyRune:
		return k.Rune == '\n' || k.Rune == '\r'
	}
	return false
}

func (t *TUI) feedKey(k Key) (bool, error) {
	t.maybeClearHint()
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.editor.wrapWidth < 1 && t.cols > 0 {
		t.editor.wrapWidth = inputWrapWidth(t.cols)
	}

	if k.isCtrlY() {
		t.copySelectionLocked()
		return false, nil
	}
	if t.sel.set {
		t.clearSelectionLocked()
	}

	if k.Kind == KeyShiftTab {
		t.toggleApprovalModeLocked()
		return false, nil
	}

	if k.isCtrlC() {
		if _, ok := t.activeOverlay.(*searchOverlay); ok {
			t.dismissOverlay()
			return false, nil
		}
		if _, ok := t.activeOverlay.(*btwOverlay); ok {
			t.dismissOverlay()
			return false, nil
		}
		if t.exitArmed {
			t.exitArmed = false
			now := time.Now()
			if !t.lastCtrlC.IsZero() && now.Sub(t.lastCtrlC) <= 2*time.Second {
				t.prepareShutdownLocked()
				return true, nil
			}
			t.lastCtrlC = now
			t.setEphemeralHintLocked("Ctrl+C again to exit", 2*time.Second)
			return false, nil
		}
	}

	// Esc cancels a running turn — but only when nothing else already owns Esc:
	// not while an approval prompt is up (Esc there means deny) and not while any
	// other overlay is open (Esc closes that first, same as it always has). Once
	// cancelled, the existing exitArmed/lastCtrlC double-tap-to-quit mechanism
	// still confirms on Ctrl+C — only the cancel trigger moved, not the quit one.
	if k.Kind == KeyEscape && t.running() && !t.approving.Load() && !t.hasKeyOverlay() {
		t.cancelActiveRunLocked()
		t.lastCtrlC = time.Now()
		return false, nil
	}

	if t.blocksBackgroundInput() || t.hasKeyOverlay() {
		if k.Kind == KeyPaste {
			if t.handleOverlayPaste(k) {
				if _, ok := t.activeOverlay.(*searchOverlay); ok {
					t.markScrollDirty()
				} else {
					t.dirty.markFull()
				}
			}
			return false, nil
		}
	}

	if t.handleKeyOverlay(k) {
		if t.overlayQuit.Load() {
			t.overlayQuit.Store(false)
			t.prepareShutdownLocked()
			return true, nil
		}
		return false, nil
	}

	if _, isSearch := t.activeOverlay.(*searchOverlay); isSearch {
		if t.searchBlocksEditorKey(k) {
			return false, nil
		}
	}

	if t.hasKeyOverlay() {
		if _, isSearch := t.activeOverlay.(*searchOverlay); !isSearch {
			if k.isCtrlC() || k.Kind == KeyEscape {
				t.dismissOverlay()
			} else {
				t.flashOverlayHintLocked()
			}
			return false, nil
		}
	}

	if !t.completion.empty() && k.Kind == KeyCtrl && !k.isCtrlC() && k.Byte != 20 {
		t.flashCompletionHintLocked()
		return false, nil
	}

	if t.focusRegion == focusConv && t.feedConvFocus(k) {
		return false, nil
	}

	w := t.contentWidth()
	if t.scroll.focusedToolExpanded(w) {
		switch k.Kind {
		case KeyArrowUp:
			if t.scroll.scrollFocusedTool(w, -1) {
				t.markScrollDirty()
			}
			return false, nil
		case KeyArrowDown:
			if t.scroll.scrollFocusedTool(w, 1) {
				t.markScrollDirty()
			}
			return false, nil
		}
	}
	if k.Kind == KeyEscape && t.scroll.collapseFocusedTool() {
		t.markScrollDirty()
		return false, nil
	}

	if !t.completion.empty() {
		switch k.Kind {
		case KeyArrowUp:
			t.completion.cycle(-1)
			t.markInputDirty()
			return false, nil
		case KeyArrowDown:
			t.completion.cycle(+1)
			t.markInputDirty()
			return false, nil
		}
	}

	if delta, ok := scrollDeltaForKey(k, t.scrollRows); ok {
		t.scrollByDelta(delta)
		return false, nil
	}

	if k.Kind == KeyTab {
		t.handleTabKey()
		return false, nil
	}

	if !t.completion.empty() && k.isEnter() {
		idx := t.completion.idx
		if idx < 0 && len(t.completion.cands) > 0 {
			idx = 0
		}
		if idx >= 0 && idx < len(t.completion.cands) {
			t.applyCompletion(t.completion.cands[idx])
			t.completion = nil
			t.markInputDirty()
			return false, nil
		}
	}

	if k.Kind == KeyEscape && t.completion != nil && !t.completion.empty() {
		t.completion = nil
		t.markInputDirty()
		return false, nil
	}

	if k.isCtrlC() {
		if t.editor.text() != "" || len(t.pendingAttachments) > 0 {
			t.editor.setText("")
			t.clearAttachments()
			t.completion = nil
			t.dirty.markFull()
		} else {
			now := time.Now()
			if !t.lastCtrlC.IsZero() && now.Sub(t.lastCtrlC) <= 2*time.Second {
				t.prepareShutdownLocked()
				return true, nil
			}
			t.lastCtrlC = now
			t.setEphemeralHintLocked("Ctrl+C again to exit", 2*time.Second)
		}
		return false, nil
	}

	if k.Kind == KeyCtrl && k.Byte == 20 {
		if toggled, collapsed := t.scroll.toggleLastThinking(); toggled {
			t.markScrollDirty()
			if collapsed {
				t.setEphemeralHintLocked("thinking: hidden", 2*time.Second)
			} else {
				t.setEphemeralHintLocked("thinking: shown", 2*time.Second)
			}
		} else {
			t.setEphemeralHintLocked("no thinking block this turn", 2*time.Second)
		}
		return false, nil
	}

	viewH := t.scrollRows
	if t.focusRegion == focusConv {
		viewH = t.convScrollRows()
	}

	if k.Kind == KeyCtrl && k.Byte == 5 {
		if t.scroll.toggleToolExpandInView(viewH, t.contentWidth()) {
			t.markScrollDirty()
		}
		return false, nil
	}

	if t.completion.empty() && t.activeOverlay == nil && k.Kind == KeyCtrl && k.Byte == 6 {
		t.openSearchLocked()
		return false, nil
	}

	if t.completion.empty() && t.activeOverlay == nil && k.Kind == KeyCtrl && (k.Byte == 16 || k.Byte == 30) {
		t.openCommandPalette()
		return false, nil
	}

	if t.completion.empty() && t.activeOverlay == nil && k.Kind == KeyCtrl && k.Byte == 7 {
		t.expediteSubagentsLocked()
		return false, nil
	}

	if t.completion.empty() && t.activeOverlay == nil && k.Kind == KeyRune && k.Rune == '.' &&
		t.editor.text() == "" && t.focusRegion == focusInput {
		t.openCommandPalette()
		return false, nil
	}

	if t.completion.empty() && t.activeOverlay == nil && k.Kind == KeyCtrl {
		switch k.Byte {
		case 2:
			t.openBTWPrompt()
			return false, nil
		case 12:
			t.openEffortPicker()
			return false, nil
		case 13:
			t.openModelPicker()
			return false, nil
		case 19:
			t.openSessionPicker()
			return false, nil
		case 18:
			t.navigateHistory(-1)
			t.markInputDirty()
			return false, nil
		case 14:
			t.navigateHistory(1)
			t.markInputDirty()
			return false, nil
		case 22: // Ctrl+V — attach an image from the clipboard
			t.grabClipboardImageAsync()
			return false, nil
		}
	}

	// Up/Down only recall history at the input's top/bottom edge; otherwise
	// they must fall through to processEditorKey below to move the cursor
	// between lines, same as any other editor key. Returning unconditionally
	// here (regardless of the boundary check) swallowed every up/down
	// keystroke in a multi-line input that wasn't already at an edge — no
	// history recall AND no cursor movement, appearing as "up/down don't do
	// anything anymore".
	if t.completion.empty() && t.activeOverlay == nil && t.focusRegion == focusInput {
		switch k.Kind {
		case KeyArrowUp:
			if t.editorAtScrollTop() {
				t.navigateHistory(-1)
				t.markInputDirty()
				return false, nil
			}
		case KeyArrowDown:
			if t.editorAtScrollBottom() {
				t.navigateHistory(1)
				t.markInputDirty()
				return false, nil
			}
		}
	}

	return t.processEditorKey(k)
}

func (t *TUI) processEditorKey(k Key) (bool, error) {
	submitted, quit := t.editor.applyKey(k)
	if submitted != "" {
		t.completion = nil
		// A turn is in flight, or a manual /compact is running (no turn, but
		// submit() itself would still reject outright — see its own
		// compacting guard): queue the message instead. It (and any other
		// queued messages) are sent once whichever is running finishes —
		// startTurn's defer for a turn, or /compact's goroutine for a manual
		// compaction.
		if t.sessionBusyLocked() && !t.approving.Load() {
			t.enqueueLocked(submitted)
			t.refreshCompletion()
			t.markInputDirty()
			return false, nil
		}
		if err := t.submit(submitted); err != nil {
			if errors.Is(err, errQuitSentinel) {
				t.prepareShutdownLocked()
				return true, nil
			}
			t.appendErrorLocked(err)
			return false, nil
		}
		t.refreshCompletion()
		t.markInputDirty()
		return false, nil
	}
	if quit {
		t.prepareShutdownLocked()
		return true, nil
	}
	t.refreshCompletion()
	t.markInputDirty()
	return false, nil
}

// toggleApprovalModeLocked flips the bash approval gate between Fast
// (deterministic guard fast path + LLM classification, the default) and
// Paranoid (every command asks a human, no auto-approval of any kind).
// Caller holds t.mu.
func (t *TUI) toggleApprovalModeLocked() {
	if t.agent == nil {
		return
	}
	next := agent.ApprovalModeParanoid
	label := "paranoid mode — every command now asks for approval"
	if t.agent.ApprovalMode() == agent.ApprovalModeParanoid {
		next = agent.ApprovalModeFast
		label = "fast mode — safe commands run automatically"
	}
	t.agent.SetApprovalMode(next)
	t.status.ApprovalMode = next
	t.setEphemeralHintLocked(label, 2*time.Second)
	t.dirty.markInput()
}

// expediteSubagentsLocked forwards the user's Ctrl+G "finish now" nudge to every
// running subagent; the main agent's own turn is left untouched. Caller holds t.mu.
func (t *TUI) expediteSubagentsLocked() {
	n := 0
	if t.agent != nil {
		n = t.agent.ExpediteSubagents()
	}
	if n == 0 {
		t.setEphemeralHintLocked("No subagents running", 2*time.Second)
		return
	}
	// Reflect the wrap-up in the pinned subagent cards so the user sees it took.
	t.scroll.markSubagentsExpediting()
	t.markScrollDirty()
	unit := "subagent"
	if n > 1 {
		unit = "subagents"
	}
	t.setEphemeralHintLocked(fmt.Sprintf("Expedited %d %s — wrapping up", n, unit), 3*time.Second)
}
