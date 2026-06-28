package tui

import (
	"strings"

	"poisson/internal/agent"
)

const toolWorkingMarker = " working..."

// layoutSnapshot is the geometry computed once per paint pass.
type layoutSnapshot struct {
	wrapWidth   int
	scrollStart int // 1-based terminal row of first scroll line
	inputTop    int
	bodyRows    int
	bodyStart   int
	hintRow     int
	firstRow    int
	sr          int
	sc          int
	screenLines []string
	visible     []ScreenRow
}

func (t *TUI) prepareLayout() layoutSnapshot {
	wrapWidth := t.cols - 1
	if wrapWidth < 1 {
		wrapWidth = 1
	}
	t.editor.wrapWidth = wrapWidth
	wantedInput := t.inputHeight(wrapWidth)
	if wantedInput != t.lastInputRows {
		t.lastInputRows = wantedInput
		t.dirty.markFull()
	}
	t.inputRows = wantedInput
	t.scrollRows = t.rows - t.headerRows - t.inputRows
	if t.scrollRows < 3 {
		t.scrollRows = 3
	}
	scrollStart := t.headerRows + 1
	inputTop := t.headerRows + t.scrollRows + 1
	bodyRows := t.inputRows - 3
	if bodyRows < 1 {
		bodyRows = 1
	}
	screenLines := wrapLines(t.editor.lines, wrapWidth)
	sr, sc := screenCursor(t.editor, wrapWidth)
	firstRow := 0
	if sr >= bodyRows {
		firstRow = sr - bodyRows + 1
	}
	return layoutSnapshot{
		wrapWidth:     wrapWidth,
		scrollStart:   scrollStart,
		inputTop:      inputTop,
		bodyRows:      bodyRows,
		bodyStart:     inputTop + 2,
		hintRow:       inputTop + 2 + bodyRows,
		firstRow:      firstRow,
		sr:            sr,
		sc:            sc,
		screenLines:   screenLines,
		visible:       t.scroll.visible(t.convScrollRows(), wrapWidth),
	}
}

func (t *TUI) paint(snap dirtySnapshot) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.status.Model = modelLabel(t.agent)
	if t.status.Branch == "" {
		t.status.Branch = gitBranch(t.status.Cwd)
	}
	t.status.SpinnerFrame = t.renderFrame

	lay := t.prepareLayout()
	if snap.full {
		t.paintFull(lay)
	} else {
		t.paintPartial(snap, lay)
	}
}

func (t *TUI) paintFull(lay layoutSnapshot) {
	var b strings.Builder
	t.paintHeaderRegion(&b, lay)
	t.paintScrollRegion(&b, lay, nil)
	t.paintInputRegion(&b, lay)
	t.paintCompletionOverlay(&b, lay)
	t.paintOverlay(&b, lay)
	t.paintCursor(&b, lay)
	t.writeRaw(b.String())
}

func (t *TUI) paintPartial(snap dirtySnapshot, lay layoutSnapshot) {
	var b strings.Builder
	if len(snap.scroll) > 0 {
		t.paintScrollRegion(&b, lay, snap.scroll)
		// Completion paints over the bottom of the scroll region; partial
		// scroll repaints must restore the dropdown on affected rows.
		if t.lastCompletionRows > 0 || (t.completion != nil && !t.completion.empty()) {
			t.paintCompletionOverlay(&b, lay)
		}
	}
	if snap.input || snap.overlay {
		t.paintInputRegion(&b, lay)
		t.paintCompletionOverlay(&b, lay)
	}
	if t.activeOverlay != nil {
		t.paintOverlay(&b, lay)
	}
	if snap.status {
		t.paintHeaderRegion(&b, lay)
	}
	if snap.cursor || snap.input || len(snap.scroll) > 0 || snap.status || snap.overlay {
		t.paintCursor(&b, lay)
	}
	if b.Len() > 0 {
		t.writeRaw(b.String())
	}
}

func (t *TUI) paintScrollRegion(b *strings.Builder, lay layoutSnapshot, only []int) {
	startRow := lay.scrollStart
	paintAll := only == nil
	onlySet := map[int]struct{}{}
	if !paintAll {
		for _, r := range only {
			onlySet[r] = struct{}{}
		}
	}
	pinRows := 0
	if t.focusRegion == focusConv {
		pinRows = 1
		if paintAll || len(onlySet) == 0 {
			b.WriteString(cup(startRow, 1))
			b.WriteString(clearLine())
			b.WriteString(truncateToWidth(t.pinnedPromptLine(lay.wrapWidth), lay.wrapWidth))
		}
	}
	contentRows := t.scrollRows - pinRows
	for i := 0; i < contentRows; i++ {
		screenRow := startRow + pinRows + i
		if !paintAll {
			if _, ok := onlySet[i+pinRows]; !ok {
				continue
			}
		}
		b.WriteString(cup(screenRow, 1))
		b.WriteString(clearLine())
		if i < len(lay.visible) {
			line := t.formatScrollLine(lay.visible[i].Text)
			line = t.applySearchHighlight(i, lay, line)
			b.WriteString(truncateToWidth(line, lay.wrapWidth))
		}
	}
}

func animateToolLine(text string, frame int) string {
	plain := stripANSI(text)
	i := strings.LastIndex(plain, toolWorkingMarker)
	if i <= 0 {
		return text
	}
	spin := spinnerChar(frame)
	// Preserve ANSI prefix from original text before the spinner rune.
	prefixPlain := plain[:i]
	if !strings.HasSuffix(prefixPlain, " ") && i > 0 {
		return text
	}
	// Rebuild: keep style prefix, swap spinner char before marker.
	styleEnd := len(text) - len(plain)
	head := text[:styleEnd]
	rest := plain[i:]
	return head + spin + rest
}

func (t *TUI) paintInputRegion(b *strings.Builder, lay layoutSnapshot) {
	b.WriteString(cup(lay.inputTop, 1))
	b.WriteString(clearLine())
	if header := t.renderInputHeader(); header != "" {
		b.WriteString(header)
	}

	b.WriteString(cup(lay.inputTop+1, 1))
	b.WriteString(clearLine())
	sep := dim + strings.Repeat("─", lay.wrapWidth) + reset
	if t.focusRegion == focusConv {
		sep = dim + strings.Repeat("·", lay.wrapWidth) + reset
	}
	b.WriteString(sep)

	for i := 0; i < lay.bodyRows; i++ {
		lineIdx := lay.firstRow + i
		b.WriteString(cup(lay.inputTop+2+i, 1))
		b.WriteString(clearLine())
		if lineIdx < len(lay.screenLines) {
			b.WriteString(t.renderInputScreenRow(lineIdx, lay.screenLines, lay.sr, lay.sc))
		}
	}

	b.WriteString(cup(lay.hintRow, 1))
	b.WriteString(clearLine())
	b.WriteString(t.renderHintLine())

	for r := lay.hintRow + 1; r <= t.rows; r++ {
		b.WriteString(cup(r, 1))
		b.WriteString(clearLine())
	}
}

func (t *TUI) paintCompletionOverlay(b *strings.Builder, lay layoutSnapshot) {
	c := t.completion
	if c == nil || c.empty() {
		if t.lastCompletionRows > 0 {
			t.paintCompletionZone(b, lay, nil, t.lastCompletionRows)
			t.lastCompletionRows = 0
		}
		return
	}
	lines := completionLines(t, c)
	t.paintCompletionZone(b, lay, lines, len(lines))
	t.lastCompletionRows = len(lines)
}

// completionLines returns the completion dropdown rows to paint (capped to scrollback).
func completionLines(t *TUI, c *completion) []string {
	all := strings.Split(strings.TrimRight(t.renderCompletion(c), "\n"), "\n")
	if len(all) <= 1 {
		return all
	}
	header := all[0]
	items := all[1:]
	maxItems := t.scrollRows - 1
	if maxItems < 1 {
		maxItems = 1
	}
	if len(items) <= maxItems {
		return all
	}
	start := 0
	if c.idx >= 0 {
		if c.idx >= maxItems-1 {
			start = c.idx - maxItems + 2
		}
		end := start + maxItems
		if end > len(items) {
			end = len(items)
			start = end - maxItems
			if start < 0 {
				start = 0
			}
		}
		items = items[start:end]
	} else {
		items = items[len(items)-maxItems:]
	}
	return append([]string{header}, items...)
}

// paintCompletionZone clears the union of the previous and current overlay
// heights, restores scrollback where the dropdown no longer covers, and paints
// the dropdown lines at the bottom of the scroll region.
func (t *TUI) paintCompletionZone(b *strings.Builder, lay layoutSnapshot, lines []string, lineCount int) {
	zone := lineCount
	if t.lastCompletionRows > zone {
		zone = t.lastCompletionRows
	}
	if zone < 1 {
		return
	}
	clearStart := lay.scrollStart + t.scrollRows - zone
	if clearStart < lay.scrollStart {
		clearStart = lay.scrollStart
	}
	anchor := lay.scrollStart + t.scrollRows - lineCount
	if anchor < lay.scrollStart {
		anchor = lay.scrollStart
	}
	for row := clearStart; row < lay.scrollStart+t.scrollRows; row++ {
		b.WriteString(cup(row, 1))
		b.WriteString(clearLine())
		if lines != nil {
			if idx := row - anchor; idx >= 0 && idx < len(lines) {
				b.WriteString(truncateToWidth(lines[idx], lay.wrapWidth))
				continue
			}
		}
		if vi := row - lay.scrollStart; vi >= 0 && vi < len(lay.visible) {
			line := t.formatScrollLine(lay.visible[vi].Text)
			line = t.applySearchHighlight(vi, lay, line)
			b.WriteString(truncateToWidth(line, lay.wrapWidth))
		}
	}
}

func (t *TUI) formatScrollLine(text string) string {
	if strings.Contains(stripANSI(text), toolWorkingMarker) {
		return animateToolLine(text, t.renderFrame)
	}
	if strings.Contains(stripANSI(text), toolCardSpinnerSlot) {
		return animateSpinnerInLine(text, t.renderFrame)
	}
	return text
}

func (t *TUI) paintOverlay(b *strings.Builder, lay layoutSnapshot) {
	if t.activeOverlay == nil {
		return
	}
	var anchor int
	var lines []string
	if b, ok := t.activeOverlay.(*btwOverlay); ok {
		anchor, lines = b.renderWithFrame(t.scrollRows, t.cols, t.renderFrame)
	} else {
		anchor, lines = t.activeOverlay.render(t.scrollRows, t.cols)
	}
	pinOffset := 0
	if _, ok := t.activeOverlay.(*searchOverlay); ok && t.focusRegion == focusConv {
		pinOffset = 1
	}
	for i, line := range lines {
		row := lay.scrollStart + anchor - 1 + pinOffset + i
		if row < lay.scrollStart || row >= lay.scrollStart+t.scrollRows {
			continue
		}
		b.WriteString(cup(row, 1))
		b.WriteString(clearLine())
		b.WriteString(truncateToWidth(line, lay.wrapWidth))
	}
}

func (t *TUI) paintHeaderRegion(b *strings.Builder, lay layoutSnapshot) {
	if t.headerRows < 1 {
		return
	}
	line := t.status.RenderHeader(lay.wrapWidth)
	for i := 0; i < t.headerRows; i++ {
		b.WriteString(cup(1+i, 1))
		b.WriteString(clearLine())
		b.WriteString(truncateToWidth(line, lay.wrapWidth))
	}
}

func (t *TUI) applySearchHighlight(vi int, lay layoutSnapshot, line string) string {
	so, ok := t.activeOverlay.(*searchOverlay)
	if !ok || so.query == "" {
		return line
	}
	_, start, _ := t.scroll.viewportRange(t.convScrollRows(), lay.wrapWidth)
	global := start + vi
	for _, m := range so.matchRows() {
		if m == global {
			if m == so.currentGlobalRow() {
				return highlightSearchMatch(line, so.query, bold+fgYellow, reset)
			}
			return highlightSearchMatch(line, so.query, fgYellow, reset)
		}
	}
	return line
}

func (t *TUI) paintCursor(b *strings.Builder, lay layoutSnapshot) {
	if t.focusRegion == focusConv {
		return
	}
	visRow := lay.sr - lay.firstRow
	if visRow < 0 {
		visRow = 0
	}
	if visRow >= lay.bodyRows {
		visRow = lay.bodyRows - 1
	}
	col := 2 + lay.sc
	if lay.sr == 0 {
		col = 3 + lay.sc
	}
	b.WriteString(cup(lay.bodyStart+visRow, col))
}

func (t *TUI) toolSpinnerRows(lay layoutSnapshot) []int {
	seen := map[int]struct{}{}
	var rows []int
	for _, i := range thinkingSpinnerRows(lay.visible) {
		seen[i] = struct{}{}
		rows = append(rows, i)
	}
	for _, i := range toolCardSpinnerRows(lay.visible) {
		if _, ok := seen[i]; !ok {
			rows = append(rows, i)
		}
	}
	for i, ln := range lay.visible {
		if _, ok := seen[i]; ok {
			continue
		}
		if strings.Contains(stripANSI(ln.Text), toolWorkingMarker) {
			rows = append(rows, i)
		}
	}
	return rows
}

func (t *TUI) markAfterEvent(ev agent.OutputEvent) {
	switch ev.Type {
	case agent.OutputText, agent.OutputThinking:
		if rows := t.scroll.streamViewportDirty(t.scrollRows, t.contentWidth()); len(rows) > 0 {
			t.dirty.markScrollRows(rows...)
		}
	case agent.OutputStatus:
		t.applyStatus(ev)
		t.dirty.markStatus()
	case agent.OutputToolStart:
		t.activeTools++
		t.dirty.markScrollAll(t.scrollRows)
	case agent.OutputToolResult:
		if t.activeTools > 0 {
			t.activeTools--
		}
		t.dirty.markScrollAll(t.scrollRows)
	case agent.OutputDone:
		t.scroll.finalizeThinking()
		t.activeTools = 0
		t.dirty.markStatus()
		t.dirty.markScrollAll(t.scrollRows)
	default:
		t.dirty.markScrollAll(t.scrollRows)
	}
}

func (t *TUI) applyStatus(ev agent.OutputEvent) {
	t.status.ContextPct = ev.ContextPct
	t.status.ContextTokens = ev.ContextTokens
	t.status.ContextWindow = ev.ContextWindow
	t.status.Cost = ev.Cost
	t.status.Model = ev.Model
	t.status.OutputTokens = ev.OutputTokens
	t.status.CacheRead = ev.CacheReadTokens
	t.status.CacheWrite = ev.CacheWriteTokens
	t.status.CallCount = ev.CallCount
	t.status.ToolCalls = ev.ToolCalls
	t.status.ToolErrors = ev.ToolErrors
	t.status.Effort = ev.Effort
	t.status.WarnContext = ev.ContextPct > 75.0
}

func (t *TUI) markScrollDirty() {
	h := t.scrollRows
	if h < 1 {
		h = 1
	}
	t.dirty.markScrollAll(h)
	if t.activeOverlay != nil {
		t.dirty.markOverlay()
	}
}

func (t *TUI) markFullDirty() {
	t.dirty.markFull()
}

func (t *TUI) markInputDirty() {
	t.dirty.markInput()
}

func (t *TUI) markSpinnerTick() {
	t.mu.Lock()
	lay := t.prepareLayout()
	rows := t.toolSpinnerRows(lay)
	if t.focusRegion == focusConv {
		for i := range rows {
			rows[i]++
		}
	}
	t.mu.Unlock()
	if len(rows) > 0 {
		t.dirty.markScrollRows(rows...)
		if t.activeOverlay != nil {
			t.dirty.markOverlay()
		}
	} else if _, ok := t.activeOverlay.(*btwOverlay); ok {
		t.dirty.markOverlay()
	} else {
		t.dirty.markStatus()
	}
}

