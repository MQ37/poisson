package tui

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
		return
	}
	if t.completion != nil && !t.completion.empty() {
		return
	}
	t.enterConvFocus()
}

func (t *TUI) feedConvFocus(data []byte) (handled bool) {
	if containsTab(data) {
		t.handleTabKey()
		return true
	}
	if delta, ok := parseScrollInputRaw(data, t.convScrollRows()); ok {
		t.scrollByDelta(delta)
		return true
	}
	if isShiftArrowLeft(data) {
		t.stepConvPrompt(-1)
		return true
	}
	if isShiftArrowRight(data) {
		t.stepConvPrompt(1)
		return true
	}
	return false
}

func containsTab(data []byte) bool {
	for _, b := range data {
		if b == 9 {
			return true
		}
	}
	return false
}