package tui

import (
	"errors"
	"time"
)

// feed decodes bytes via the shared key decoder and dispatches each event.
// Tests may call this directly; the input loop uses feedKey per decoded key.
func (t *TUI) feed(data []byte) (bool, error) {
	for _, k := range t.keyDec.Push(data) {
		if quit, err := t.feedKey(k); quit || err != nil {
			return quit, err
		}
	}
	return false, nil
}

// feedKey handles one normalized key event. It returns (quit, error).
func (t *TUI) feedKey(k Key) (bool, error) {
	t.maybeClearHint()
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.editor.wrapWidth < 1 && t.cols > 0 {
		w := t.cols - 1
		if w < 1 {
			w = 1
		}
		t.editor.wrapWidth = w
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
			return true, nil
		}
		return false, nil
	}

	if t.hasKeyOverlay() {
		if _, isSearch := t.activeOverlay.(*searchOverlay); !isSearch {
			if k.isCtrlC() || k.Kind == KeyEscape {
				t.dismissOverlay()
			}
			return false, nil
		}
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
		if t.running() && !t.approving.Load() {
			t.cancelActiveRunLocked()
			t.lastCtrlC = time.Now()
			return false, nil
		}
		if t.editor.text() != "" {
			t.editor.setText("")
			t.completion = nil
			t.markInputDirty()
		} else {
			now := time.Now()
			if !t.lastCtrlC.IsZero() && now.Sub(t.lastCtrlC) <= 2*time.Second {
				return true, nil
			}
			t.lastCtrlC = now
			t.setEphemeralHintLocked("Ctrl+C again to exit", 2*time.Second)
		}
		return false, nil
	}

	viewH := t.scrollRows
	if t.focusRegion == focusConv {
		viewH = t.convScrollRows()
	}

	if k.Kind == KeyCtrl && k.Byte == 20 {
		if t.scroll.toggleThinkingInView(viewH, t.contentWidth()) {
			t.markScrollDirty()
		}
		return false, nil
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

	if k.Kind == KeyCtrl && k.Byte == 25 {
		t.yankClipboardLocked()
		return false, nil
	}

	if t.completion.empty() && t.activeOverlay == nil && k.Kind == KeyCtrl && (k.Byte == 16 || k.Byte == 30) {
		t.openCommandPalette()
		return false, nil
	}

	if t.completion.empty() && t.activeOverlay == nil && k.Kind == KeyCtrl {
		switch k.Byte {
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
		}
	}

	if t.completion.empty() && t.activeOverlay == nil && t.focusRegion == focusInput {
		switch k.Kind {
		case KeyArrowUp:
			if t.editorAtScrollTop() {
				t.navigateHistory(-1)
				t.markInputDirty()
			}
			return false, nil
		case KeyArrowDown:
			if t.editorAtScrollBottom() {
				t.navigateHistory(1)
				t.markInputDirty()
			}
			return false, nil
		}
	}

	if t.running() && !t.approving.Load() {
		if k.isEnter() {
			return false, nil
		}
		return t.processEditorKey(k)
	}

	return t.processEditorKey(k)
}

// processEditorKey applies one key to the editor and handles submit/quit.
func (t *TUI) processEditorKey(k Key) (bool, error) {
	submitted, quit := t.editor.applyKey(k)
	if submitted != "" {
		t.completion = nil
		if err := t.submit(submitted); err != nil {
			if errors.Is(err, errQuitSentinel) {
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
		return true, nil
	}
	t.refreshCompletion()
	t.markInputDirty()
	return false, nil
}

// processEditor feeds legacy raw bytes to the editor (tests only).
func (t *TUI) processEditor(data []byte) (bool, error) {
	for _, k := range (&Decoder{}).Push(data) {
		if quit, err := t.processEditorKey(k); quit || err != nil {
			return quit, err
		}
	}
	return false, nil
}

// hasCSI reports whether data contains any CSI sequence (ESC[).
func hasCSI(data []byte) bool {
	for i := 0; i+1 < len(data); i++ {
		if data[i] == 27 && data[i+1] == '[' {
			return true
		}
	}
	return false
}

// containsCtrlC reports whether data contains a Ctrl+C byte outside a
// bracketed-paste region. A pasted 0x03 should not exit the editor.
func containsCtrlC(data []byte) bool {
	inPaste := false
	for i := 0; i < len(data); i++ {
		b := data[i]
		if !inPaste && i+5 < len(data) && b == 27 && data[i+1] == '[' && data[i+2] == '2' && data[i+3] == '0' && data[i+4] == '0' && data[i+5] == '~' {
			inPaste = true
			i += 5
			continue
		}
		if inPaste && i+5 < len(data) && b == 27 && data[i+1] == '[' && data[i+2] == '2' && data[i+3] == '0' && data[i+4] == '1' && data[i+5] == '~' {
			inPaste = false
			i += 5
			continue
		}
		if !inPaste && b == 3 {
			return true
		}
	}
	return false
}

// containsSubmitKey reports whether data contains a plain Enter/Return key.
// Handles both \r (raw) and kitty keyboard Enter (ESC[13u or ESC[13;1u).
// Shift+Enter (ESC[13;2u) does NOT match.
func containsSubmitKey(data []byte) bool {
	for i, b := range data {
		if b == '\r' {
			return true
		}
		// kitty: ESC [ 1 3 u  (plain Enter)
		if b == 27 && i+4 < len(data) && data[i+1] == '[' && data[i+2] == '1' && data[i+3] == '3' && data[i+4] == 'u' {
			return true
		}
		// kitty: ESC [ 1 3 ; 1 u  (plain Enter, explicit mods)
		if b == 27 && i+6 < len(data) && data[i+1] == '[' && data[i+2] == '1' && data[i+3] == '3' && data[i+4] == ';' && data[i+5] == '1' && data[i+6] == 'u' {
			return true
		}
	}
	return false
}