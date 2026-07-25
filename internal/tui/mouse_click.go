package tui

import "strings"

// maxMouseBufBytes bounds the carry-over buffer for a partial mouse sequence.
// A complete SGR report is well under this; a stream that never completes
// (foreign/malformed bytes) gets dropped instead of buffered forever.
const maxMouseBufBytes = 64

// handleMouseInput consumes pure mouse reports (wheel, click, drag-select),
// reassembling sequences that a read() split mid-escape-code. Returns true if
// the input goroutine should skip further processing of data.
func (t *TUI) handleMouseInput(data []byte) bool {
	buf := data
	if len(t.mouseBuf) > 0 {
		buf = append(append([]byte(nil), t.mouseBuf...), data...)
	}
	if !hasMousePrefix(buf) {
		return false
	}
	events, rest := consumeMouseEvents(buf)
	if hasMousePrefix(rest) && len(rest) <= maxMouseBufBytes {
		t.mouseBuf = rest
	} else {
		// rest is either empty, or bytes that turned out not to be a mouse
		// sequence continuation (or an unreasonably long non-completing one) —
		// nothing more to carry over.
		t.mouseBuf = nil
	}
	for _, ev := range events {
		t.handleOneMouseEvent(ev)
	}
	return true
}

// hasMousePrefix reports whether data opens an SGR mouse report (CSI <).
func hasMousePrefix(data []byte) bool {
	return len(data) >= 3 && data[0] == 27 && data[1] == '[' && data[2] == '<'
}

// consumeMouseEvents parses as many complete SGR mouse reports as possible
// from the front of data, returning them plus whatever's left (either empty,
// an incomplete trailing sequence to carry over, or non-mouse bytes once a
// mixed chunk stops looking like a mouse report).
func consumeMouseEvents(data []byte) (events []MouseEvent, rest []byte) {
	for len(data) > 0 && hasMousePrefix(data) {
		adv := advancePastMouse(data)
		if adv <= 0 {
			break // incomplete trailing sequence; caller carries it over
		}
		events = append(events, parseMouseEvents(data[:adv])...)
		data = data[adv:]
	}
	return events, data
}

// handleOneMouseEvent dispatches a single parsed mouse report: wheel scroll,
// left-button press/drag/release (click or text selection), or an ignored
// button/state.
func (t *TUI) handleOneMouseEvent(ev MouseEvent) {
	t.mu.Lock()

	if delta, ok := mouseWheelDelta(ev.Button); ok {
		if t.approving.Load() {
			// The approval panel only occupies the bottom input region — the
			// conversation stays visible above it (same as PgUp/PgDn, see
			// approvalRoutesToHandler). A wheel event landing on the panel
			// itself scrolls the (possibly long) command text; anywhere
			// above that, in the still-visible conversation, it must scroll
			// the conversation instead — previously every wheel tick during
			// an approval was routed to the panel unconditionally, so
			// scrolling the mouse over the conversation while a prompt was
			// pending silently did nothing (users had to reach for PgUp/PgDn).
			panelTop := t.headerRows + t.scrollRows + 1
			if ao, ok := t.activeOverlay.(*approvalOverlay); ok && ev.Row >= panelTop {
				// Wheel-up shows earlier command lines (opposite of scrollback delta).
				ao.scrollBy(-delta)
				t.dirty.markInput()
				t.mu.Unlock()
				return
			}
			t.mu.Unlock()
			t.handleScrollDelta(delta)
			return
		}
		if t.blocksBackgroundInput() {
			if flo, ok := t.activeOverlay.(*filterableListOverlay); ok {
				vis := flo.filtered()
				if delta > 0 && flo.idx > 0 {
					flo.idx--
				} else if delta < 0 && flo.idx < len(vis)-1 {
					flo.idx++
				}
				t.dirty.markFull()
				t.mu.Unlock()
				return
			}
			t.mu.Unlock()
			return
		}
		w := t.contentWidth()
		if t.scroll.focusedToolExpanded(w) {
			// Wheel-up shows earlier lines (opposite of scrollFocusedTool's own
			// delta convention, same fix as the approval overlay above).
			if t.scroll.scrollFocusedTool(w, -delta) {
				t.markScrollDirty()
			}
			t.mu.Unlock()
			return
		}
		t.mu.Unlock()
		t.handleScrollDelta(delta)
		return
	}

	if ev.Button == 0 && ev.Press && !ev.Motion {
		t.beginPressLocked(ev.Row, ev.Col)
		t.mu.Unlock()
		return
	}
	if ev.Button&3 == 0 && ev.Motion {
		t.extendSelectionLocked(ev.Row, ev.Col)
		t.mu.Unlock()
		return
	}
	if ev.Button&3 == 0 && !ev.Press && !ev.Motion {
		t.endSelectionLocked(ev.Row)
		t.mu.Unlock()
		return
	}

	t.mu.Unlock() // consume other buttons / states
}

// dispatchOverlayClickLocked handles a click landing on overlay chrome (list
// picker, completion dropdown, search box) or blocked by a non-search
// overlay. Returns true if it fully handled the click — drag-select never
// applies to overlay chrome. Must be called with t.mu held.
func (t *TUI) dispatchOverlayClickLocked(row int) bool {
	if lo := asListClickOverlay(t.activeOverlay); lo != nil {
		chrome := lo.listChrome()
		scrollStart := t.headerRows + 1
		lineInOverlay := row - scrollStart - chrome.anchor + 1 - t.overlayPinOffset()
		prev := t.activeOverlay
		if handled, done := lo.clickRow(lineInOverlay); handled {
			if !done {
				t.dirty.markFull()
			}
			t.closeOverlayAfter(prev, done, false)
			return true
		}
		if chrome.totalLines > 0 && (lineInOverlay < 0 || lineInOverlay >= chrome.totalLines) {
			t.dismissOverlay()
		}
		return true
	}

	if t.handleCompletionClick(row) {
		return true
	}

	if so, ok := t.activeOverlay.(*searchOverlay); ok {
		anchor, lines := so.render(t.scrollRows, t.cols)
		searchRow := t.headerRows + 1 + anchor - 1 + t.overlayPinOffset()
		if row >= searchRow && row < searchRow+len(lines) {
			return true
		}
	}

	if t.activeOverlay != nil {
		_, isSearch := t.activeOverlay.(*searchOverlay)
		_, isApproval := t.activeOverlay.(*approvalOverlay)
		// The approval panel only occupies the bottom input region — same as
		// the wheel-scroll exemption above — so a click landing in the still-
		// visible conversation above it must reach the normal handler (to
		// toggle a thinking/tool-card block) instead of being swallowed as
		// "click blocked by overlay". handleMouseClickLocked's own row bounds
		// check already confines this to the conversation area; a click on
		// the panel itself still falls through to no-op there.
		if !isSearch && !isApproval {
			return true
		}
	}

	return false
}

func (t *TUI) handleMouseClick(row int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.handleMouseClickLocked(row)
}

// handleMouseClickLocked toggles thinking/tool-card expand for a plain click
// (no drag) inside the conversation area. Must be called with t.mu held.
func (t *TUI) handleMouseClickLocked(row int) {
	if t.dispatchOverlayClickLocked(row) {
		return
	}

	scrollStart := t.headerRows + 1 // 1-based first scroll row
	scrollEnd := t.headerRows + t.scrollRows
	if row < scrollStart || row > scrollEnd {
		return
	}
	pinRows := t.convPinRows()
	viewH := t.convScrollRows()
	vi := row - scrollStart - pinRows
	if vi < 0 {
		return
	}
	if t.scroll.clickBlockAt(viewH, t.contentWidth(), vi) {
		t.markScrollDirty()
	}
}

// handleCompletionClick selects a completion item when the user clicks the dropdown.
func (t *TUI) handleCompletionClick(row int) bool {
	c := t.completion
	if c == nil || c.empty() || t.lastCompletionRows == 0 {
		return false
	}
	lines := completionLines(t, c)
	if len(lines) == 0 {
		return false
	}
	anchor := t.headerRows + t.scrollRows - len(lines) + 1
	if row < anchor || row >= anchor+len(lines) {
		return false
	}
	lineIdx := row - anchor
	if lineIdx == 0 {
		return true
	}
	plain := stripANSI(lines[lineIdx])
	plain = strings.TrimSpace(strings.TrimPrefix(plain, "▶"))
	for _, cand := range c.cands {
		if plain == cand || strings.HasSuffix(plain, cand) {
			t.applyCompletion(cand)
			t.completion = nil
			t.markInputDirty()
			return true
		}
	}
	return true
}

// clickBlockAt toggles thinking collapse or tool expand for the block at
// viewport row vi (0-based within the scroll region).
func (s *scrollback) clickBlockAt(height, width, vi int) bool {
	visible := s.visible(height, width)
	if vi < 0 || vi >= len(visible) {
		return false
	}
	id := visible[vi].Tag.BlockID
	for i := range s.blocks {
		b := &s.blocks[i]
		if b.id != id {
			continue
		}
		switch b.kind {
		case blockThinking:
			if b.meta.Streaming {
				return false
			}
			b.meta.Collapsed = !b.meta.Collapsed
			b.invalidateLayout()
			return true
		case blockToolCall:
			return s.toggleToolExpandBlock(id)
		default:
			return false
		}
	}
	return false
}
