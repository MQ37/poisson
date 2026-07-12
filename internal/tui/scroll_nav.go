package tui

// parseMouseWheelScroll reports SGR mouse-wheel scroll delta when the chunk is
// a lone wheel event. Keyboard scroll (Page Up/Down, shift+arrows) is handled
// via Decoder.Push → scrollDeltaForKey in feedKey.
func parseMouseWheelScroll(data []byte) (delta int, ok bool) {
	return parseMouseWheel(data)
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
