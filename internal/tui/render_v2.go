package tui

import (
	"fmt"
	"strings"

	"poisson/internal/agent"
)

const toolWorkingMarker = " working..."

// layoutSnapshot is the geometry computed once per paint pass.
type layoutSnapshot struct {
	wrapWidth   int
	scrollStart int // 1-based terminal row of first scroll line
	inputTop    int
	attachRows  int // image-chip rows, between separator and queued preview
	queuedRows  int // pending-message preview rows, between separator and body
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
	wrapWidth := inputWrapWidth(t.cols)
	t.editor.wrapWidth = wrapWidth
	wantedInput := t.inputHeight(wrapWidth)
	if wantedInput != t.lastInputRows {
		t.lastInputRows = wantedInput
		t.dirty.markFull()
		// markFull only takes effect on the NEXT dirty.consume() — too late for
		// whichever paint() call is running prepareLayout right now. Without this,
		// a partial repaint proceeds with the NEW row positions (e.g. the
		// separator moving down a row because the input shrank) while the OLD
		// separator's row, now just above it, never gets touched: it's outside
		// both the new input region (which starts lower) and this partial
		// repaint's untouched scroll region, so it lingers until the next tick
		// — a duplicated separator for one frame, or longer under a slow/laggy
		// terminal. layoutJustChanged lets THIS call's paint() upgrade itself to
		// a full repaint immediately instead of waiting a tick to self-heal.
		t.layoutJustChanged = true
	}
	t.inputRows = wantedInput
	t.scrollRows = t.rows - t.headerRows - t.inputRows
	if t.scrollRows < 3 {
		t.scrollRows = 3
	}
	scrollStart := t.headerRows + 1
	inputTop := t.headerRows + t.scrollRows + 1
	queuedRows := 0
	attachRows := 0
	if !t.approving.Load() {
		queuedRows = t.queuedPreviewRows()
		attachRows = t.attachmentRows()
	}
	bodyRows := t.inputRows - 2 - queuedRows - attachRows
	if bodyRows < 1 {
		bodyRows = 1
	}
	bodyStart := inputTop + 1 + attachRows + queuedRows
	screenLines := wrapLines(t.editor.lines, wrapWidth)
	sr, sc := screenCursor(t.editor, wrapWidth)
	firstRow := 0
	if sr >= bodyRows {
		firstRow = sr - bodyRows + 1
	}
	return layoutSnapshot{
		wrapWidth:   wrapWidth,
		scrollStart: scrollStart,
		inputTop:    inputTop,
		attachRows:  attachRows,
		queuedRows:  queuedRows,
		bodyRows:    bodyRows,
		bodyStart:   bodyStart,
		hintRow:     bodyStart + bodyRows,
		firstRow:    firstRow,
		sr:          sr,
		sc:          sc,
		screenLines: screenLines,
		// Scrollback wraps at contentWidth, not the (narrower) input wrapWidth.
		visible: t.scroll.visible(t.convScrollRows(), t.contentWidth()),
	}
}

func (t *TUI) paint(snap dirtySnapshot) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.syncHeaderFromAgentLocked()
	t.updateWindowTitleLocked()
	if !t.branchChecked {
		t.branchChecked = true
		t.status.Branch = gitBranch(t.status.Cwd)
	}
	t.status.SpinnerFrame = t.renderFrame

	lay := t.prepareLayout()
	if t.layoutJustChanged {
		t.layoutJustChanged = false
		snap.full = true
	}
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
	if t.activeOverlay != nil || t.lastOverlayLines > 0 {
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
	pinRows := t.convPinRows()
	// Pinned region above the conversation: running subagent widgets first
	// (live spinner + timer), then the focus-mode prompt line (if any).
	subLines := t.scroll.runningSubagentLines(lay.wrapWidth)
	for pr := 0; pr < pinRows; pr++ {
		_, pinDirty := onlySet[pr]
		if !(paintAll || pinDirty) {
			continue
		}
		if pr < len(subLines) {
			b.WriteString(cup(startRow+pr, 1))
			b.WriteString(clearLine())
			b.WriteString(truncateToWidth(t.formatScrollLine(subLines[pr]), lay.wrapWidth))
			continue
		}
		t.paintConvPinRow(b, startRow+pr, t.cols)
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
			b.WriteString(t.formatVisibleScrollLineTrunc(i, lay))
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
	if t.approving.Load() {
		if ao, ok := t.activeOverlay.(*approvalOverlay); ok {
			t.paintApprovalInputRegion(b, lay, ao)
			return
		}
	}

	// Separator on the first input row (the previously-empty header row was
	// reclaimed to keep the input area compact).
	b.WriteString(cup(lay.inputTop, 1))
	b.WriteString(clearLine())
	sep := dim + strings.Repeat("─", lay.wrapWidth) + reset
	if t.focusRegion == focusConv {
		sep = dim + strings.Repeat("·", lay.wrapWidth) + reset
	}
	b.WriteString(sep)

	if lay.attachRows > 0 {
		b.WriteString(cup(lay.inputTop+1, 1))
		b.WriteString(clearLine())
		b.WriteString(truncateToWidth(t.renderAttachmentRow(lay.wrapWidth), lay.wrapWidth))
	}

	for i := 0; i < lay.queuedRows; i++ {
		b.WriteString(cup(lay.inputTop+1+lay.attachRows+i, 1))
		b.WriteString(clearLine())
		b.WriteString(truncateToWidth(t.renderQueuedRow(i, lay.wrapWidth), lay.wrapWidth))
	}

	for i := 0; i < lay.bodyRows; i++ {
		lineIdx := lay.firstRow + i
		b.WriteString(cup(lay.bodyStart+i, 1))
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

// renderQueuedRow renders one pending-message preview line. When there are more
// queued messages than preview rows, the last row summarises the remainder.
func (t *TUI) renderQueuedRow(i, width int) string {
	n := len(t.queued)
	if n > maxQueuedPreview && i == maxQueuedPreview-1 {
		return dim + fmt.Sprintf("  … +%d more queued", n-(maxQueuedPreview-1)) + reset
	}
	if i >= n {
		return ""
	}
	label := "  ⏳ "
	avail := width - visibleWidth(label)
	if avail < 1 {
		avail = 1
	}
	return dim + label + truncatePlain(collapseWhitespace(t.queued[i]), avail) + reset
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
		pinRows := t.convPinRows()
		if pinRows > 0 && row == lay.scrollStart {
			t.paintConvPinRow(b, row, t.cols)
			continue
		}
		vi := row - lay.scrollStart - pinRows
		if vi >= 0 && vi < len(lay.visible) {
			b.WriteString(t.formatVisibleScrollLineTrunc(vi, lay))
		}
	}
}

func (t *TUI) paintConvPinRow(b *strings.Builder, row, width int) {
	b.WriteString(cup(row, 1))
	b.WriteString(clearLine())
	b.WriteString(t.pinnedPromptLine(width))
}

func (t *TUI) formatVisibleScrollLine(vi int, lay layoutSnapshot) string {
	line := t.formatScrollLine(lay.visible[vi].Text)
	line = t.applySearchHighlight(vi, lay, line)
	return t.applySelectionHighlight(vi, line)
}

// formatVisibleScrollLineTrunc renders and truncates a scrollback row at
// contentWidth — the width scrollback rows actually wrap at (layoutAll, click
// hit-testing, and selection all agree on contentWidth; lay.wrapWidth is
// narrower, sized for the input editor's "› " prompt gutter).
func (t *TUI) formatVisibleScrollLineTrunc(vi int, lay layoutSnapshot) string {
	return truncateToWidth(t.formatVisibleScrollLine(vi, lay), t.contentWidth())
}

// applySelectionHighlight reverse-videos the portion of viewport row vi that
// falls inside the current mouse text selection, if any.
func (t *TUI) applySelectionHighlight(vi int, line string) string {
	if !t.sel.set {
		return line
	}
	loRow, loCol, hiRow, hiCol := t.sel.bounds()
	global := t.scroll.viewportStart(t.convScrollRows(), t.contentWidth()) + vi
	if global < loRow || global > hiRow {
		return line
	}
	start, end := 0, len([]rune(stripANSI(line)))
	if global == loRow {
		start = loCol
	}
	if global == hiRow {
		end = hiCol + 1
	}
	if start >= end {
		return line
	}
	return highlightPlainSpans(line, [][2]int{{start, end}}, reverseVideo, reset)
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
	if _, ok := t.activeOverlay.(*approvalOverlay); ok {
		if t.lastOverlayLines > 0 {
			t.clearOverlayGhostRows(b, lay, 1, t.lastOverlayLines)
			t.lastOverlayLines = 0
		}
		return
	}
	if t.activeOverlay == nil {
		if t.lastOverlayLines > 0 {
			t.clearOverlayGhostRows(b, lay, 1, t.lastOverlayLines)
			t.lastOverlayLines = 0
		}
		return
	}
	var anchor int
	var lines []string
	if b, ok := t.activeOverlay.(*btwOverlay); ok {
		anchor, lines = b.renderWithFrame(t.scrollRows, t.cols, t.renderFrame)
	} else {
		anchor, lines = t.activeOverlay.render(t.scrollRows, t.cols)
	}
	pinOffset := t.overlayPinOffset()
	height := len(lines)
	if t.lastOverlayLines > height {
		t.clearOverlayGhostRows(b, lay, anchor+pinOffset+height, t.lastOverlayLines-height)
	}
	for i, line := range lines {
		row := lay.scrollStart + anchor - 1 + pinOffset + i
		if row < lay.scrollStart || row >= lay.scrollStart+t.scrollRows {
			continue
		}
		b.WriteString(cup(row, 1))
		b.WriteString(clearLine())
		b.WriteString(truncateToWidth(line, t.cols))
	}
	t.lastOverlayLines = height
}

// clearOverlayGhostRows erases leftover overlay rows after the overlay shrinks or closes.
func (t *TUI) clearOverlayGhostRows(b *strings.Builder, lay layoutSnapshot, startRow, count int) {
	for i := 0; i < count; i++ {
		row := lay.scrollStart + startRow - 1 + i
		if row < lay.scrollStart || row >= lay.scrollStart+t.scrollRows {
			continue
		}
		b.WriteString(cup(row, 1))
		b.WriteString(clearLine())
		pinRows := t.convPinRows()
		if pinRows > 0 && row == lay.scrollStart {
			t.paintConvPinRow(b, row, t.cols)
			continue
		}
		vi := row - lay.scrollStart - pinRows
		if vi >= 0 && vi < len(lay.visible) {
			b.WriteString(t.formatVisibleScrollLineTrunc(vi, lay))
		}
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
	_, start, _ := t.scroll.viewportRange(t.convScrollRows(), t.contentWidth())
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
	if t.approving.Load() || t.blocksBackgroundInput() {
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
	case agent.OutputText:
		viewH := t.convScrollRows()
		if rows := t.scroll.streamViewportDirty(viewH, t.contentWidth()); len(rows) > 0 {
			t.dirty.markScrollRows(t.offsetConvDirtyRows(rows)...)
		}
	case agent.OutputThinking:
		t.markScrollDirty()
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
	case agent.OutputCompacted:
		t.dirty.markStatus()
	case agent.OutputDone:
		t.scroll.finalizeThinking()
		t.activeTools = 0
		if t.turnCancelled {
			t.turnCancelled = false
			t.scroll.appendRaw(styleSystem, "  cancelled")
		}
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
	if pin := t.convPinRows(); pin > 0 {
		for i := 0; i < pin; i++ {
			t.dirty.markScrollRows(i)
		}
	}
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
	rows := t.offsetConvDirtyRows(t.toolSpinnerRows(lay))
	runningSub := t.scroll.hasRunningSubagent()
	thinking := t.status.Thinking
	t.mu.Unlock()
	// While a subagent runs, repaint the whole scroll region so the pinned
	// running-agent lines (spinner + live timer) update each tick.
	if runningSub {
		t.dirty.markScrollAll(t.scrollRows)
	} else if len(rows) > 0 {
		t.dirty.markScrollRows(rows...)
	}
	if t.activeOverlay != nil {
		t.dirty.markOverlay()
	}
	// Always refresh the status bar so the header spinner next to the model
	// keeps spinning even while tool/subagent cards are animating.
	if thinking {
		t.dirty.markStatus()
	}
}
