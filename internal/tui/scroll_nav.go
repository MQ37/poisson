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
	if indexOf(data, []byte("\x1b[5~")) >= 0 { // PageUp
		return viewHeight, true
	}
	if indexOf(data, []byte("\x1b[6~")) >= 0 { // PageDown
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

// parseMouseWheel parses SGR mouse wheel sequences (DECSET 1006).
// Wheel up scrolls history up (positive delta).
func parseMouseWheel(data []byte) (delta int, ok bool) {
	if len(data) < 6 || data[0] != 27 || data[1] != '[' || data[2] != '<' {
		return 0, false
	}
	i := 3
	btn := 0
	for i < len(data) && data[i] >= '0' && data[i] <= '9' {
		btn = btn*10 + int(data[i]-'0')
		i++
	}
	if i >= len(data) || (data[i] != ';' && data[i] != 'M' && data[i] != 'm') {
		return 0, false
	}
	if data[i] == ';' {
		for i < len(data) && data[i] != 'M' && data[i] != 'm' {
			i++
		}
	}
	if i >= len(data) {
		return 0, false
	}
	consumed := i + 1
	switch btn {
	case 64: // wheel up
		return 3, true
	case 65: // wheel down
		return -3, true
	default:
		_ = consumed
		return 0, false
	}
}

// isArrowUp reports a plain Up-arrow CSI sequence (no shift modifier).
func isArrowUp(data []byte) bool {
	return indexOf(data, []byte("\x1b[A")) >= 0 ||
		indexOf(data, []byte("\x1bOA")) >= 0
}

// isArrowDown reports a plain Down-arrow CSI sequence.
func isArrowDown(data []byte) bool {
	return indexOf(data, []byte("\x1b[B")) >= 0 ||
		indexOf(data, []byte("\x1bOB")) >= 0
}