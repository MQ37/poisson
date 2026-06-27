package tui

// parseScrollInput returns how many screen rows to scroll up (positive) or down
// (negative) and whether the input was a scroll gesture.
func parseScrollInput(data []byte, viewHeight int) (delta int, ok bool) {
	if viewHeight < 1 {
		viewHeight = 1
	}
	if n, ok := parseMouseWheel(data); ok {
		return n, true
	}
	if isPageUp(data) {
		return viewHeight, true
	}
	if isPageDown(data) {
		return -viewHeight, true
	}
	// Shift+arrow (common terminal encoding).
	if indexOf(data, []byte("\x1b[1;2A")) >= 0 {
		return 3, true
	}
	if indexOf(data, []byte("\x1b[1;2B")) >= 0 {
		return -3, true
	}
	return 0, false
}

// isShiftArrowScroll reports shift+up/down scroll gestures.
func isShiftArrowScroll(data []byte) bool {
	return indexOf(data, []byte("\x1b[1;2A")) >= 0 ||
		indexOf(data, []byte("\x1b[1;2B")) >= 0
}

// parseMouseWheel parses SGR mouse wheel sequences (DECSET 1006).
// Wheel up scrolls history up (positive delta).
func parseMouseWheel(data []byte) (delta int, ok bool) {
	events := parseMouseEvents(data)
	if len(events) != 1 || len(data) > advancePastMouse(data) {
		// Only treat as wheel when the chunk is a single mouse event.
		if !dataIsOnlyMouse(data) || len(events) != 1 {
			return 0, false
		}
	}
	return mouseWheelDelta(events[0].Button)
}

func isPageUp(data []byte) bool {
	return indexOf(data, []byte("\x1b[5~")) >= 0 ||
		indexOf(data, []byte("\x1b[5;")) >= 0
}

func isPageDown(data []byte) bool {
	return indexOf(data, []byte("\x1b[6~")) >= 0 ||
		indexOf(data, []byte("\x1b[6;")) >= 0
}

// isArrowUp reports a plain Up-arrow (legacy CSI or kitty CSI-u).
func isArrowUp(data []byte) bool {
	data = decodeKittyKeys(data)
	for _, seq := range [][]byte{
		[]byte("\x1b[A"), []byte("\x1bOA"),
		[]byte("\x1b[1;1A"), []byte("\x1b[1A"),
	} {
		if indexOf(data, seq) >= 0 {
			return true
		}
	}
	return isKittyArrowKey(data, kittyKeyUp, kittyKeyKPUp)
}

// isArrowDown reports a plain Down-arrow (legacy CSI or kitty CSI-u).
func isArrowDown(data []byte) bool {
	data = decodeKittyKeys(data)
	for _, seq := range [][]byte{
		[]byte("\x1b[B"), []byte("\x1bOB"),
		[]byte("\x1b[1;1B"), []byte("\x1b[1B"),
	} {
		if indexOf(data, seq) >= 0 {
			return true
		}
	}
	return isKittyArrowKey(data, kittyKeyDown, kittyKeyKPDown)
}

// isKittyArrowKey matches kitty keyboard protocol arrow keys (plain, no ctrl/alt).
func isKittyArrowKey(data []byte, codes ...int) bool {
	for i := 0; i < len(data); i++ {
		if data[i] != 27 || i+1 >= len(data) || data[i+1] != '[' {
			continue
		}
		code, mods, final, n := parseKittyKey(data[i:])
		if n == 0 || final != 'u' {
			continue
		}
		m := mods - 1
		if m < 0 {
			m = 0
		}
		// Ignore ctrl/alt/super; allow plain (0) or shift-only for navigation.
		if m&^1 != 0 {
			continue
		}
		for _, want := range codes {
			if code == want {
				return true
			}
		}
	}
	return false
}