package tui

// handleMouseInput consumes pure mouse reports (wheel + click). Returns true if
// the input goroutine should skip further processing.
func (t *TUI) handleMouseInput(data []byte) bool {
	if !dataIsOnlyMouse(data) {
		return false
	}
	events := parseMouseEvents(data)
	if len(events) == 0 {
		return false
	}
	ev := events[len(events)-1]

	if delta, ok := mouseWheelDelta(ev.Button); ok {
		t.mu.Lock()
		block := t.blocksBackgroundInput()
		t.mu.Unlock()
		if !block {
			t.handleScrollDelta(delta)
		}
		return true
	}

	if ev.Button == 0 && ev.Press {
		t.handleMouseClick(ev.Row)
		return true
	}

	return true // consume release / other buttons
}

func (t *TUI) handleMouseClick(row int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if lo := asListClickOverlay(t.activeOverlay); lo != nil {
		chrome := lo.listChrome()
		scrollStart := t.headerRows + 1
		lineInOverlay := row - scrollStart - chrome.anchor + 1
		prev := t.activeOverlay
		if handled, done := lo.clickRow(lineInOverlay); handled {
			t.closeOverlayAfter(prev, done, false)
		}
		return
	}

	if t.activeOverlay != nil {
		return
	}

	scrollStart := t.headerRows + 1 // 1-based first scroll row
	scrollEnd := t.headerRows + t.scrollRows
	if row < scrollStart || row > scrollEnd {
		return
	}
	vi := row - scrollStart
	if t.scroll.clickBlockAt(t.scrollRows, t.contentWidth(), vi) {
		t.markScrollDirty()
	}
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