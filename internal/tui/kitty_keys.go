package tui

// Kitty keyboard protocol functional key codes (PUA 57344+).
// See kitty/key_encoding.py functional_key_number_to_name_map.
const (
	kittyKeyEscape   = 57344
	kittyKeyEnter    = 57345
	kittyKeyTab      = 57346
	kittyKeyBackspace = 57347
	kittyKeyDelete   = 57348
	kittyKeyInsert   = 57349
	kittyKeyUp       = 57352
	kittyKeyDown     = 57353
	kittyKeyPageUp   = 57354
	kittyKeyPageDown = 57355
	kittyKeyKPUp     = 57419
	kittyKeyKPDown   = 57420
	kittyKeyKPPageUp = 57421
	kittyKeyKPPageDn = 57422
)

// kittyToLegacyCSI maps kitty CSI-u functional keys to legacy xterm sequences
// that the rest of the input pipeline already understands.
func kittyToLegacyCSI(code, mods int) ([]byte, bool) {
	m := mods - 1
	if m < 0 {
		m = 0
	}
	shift := m&1 != 0
	switch code {
	case kittyKeyUp, kittyKeyKPUp:
		if shift {
			return []byte("\x1b[1;2A"), true
		}
		return []byte("\x1b[A"), true
	case kittyKeyDown, kittyKeyKPDown:
		if shift {
			return []byte("\x1b[1;2B"), true
		}
		return []byte("\x1b[B"), true
	case kittyKeyPageUp, kittyKeyKPPageUp:
		return []byte("\x1b[5~"), true
	case kittyKeyPageDown, kittyKeyKPPageDn:
		return []byte("\x1b[6~"), true
	case 57350: // LEFT
		return []byte("\x1b[D"), true
	case 57351: // RIGHT
		return []byte("\x1b[C"), true
	case 57356, 57423: // HOME, KP_HOME
		return []byte("\x1b[H"), true
	case 57357, 57424: // END, KP_END
		return []byte("\x1b[F"), true
	case kittyKeyDelete:
		return []byte("\x1b[3~"), true
	case kittyKeyInsert:
		return []byte("\x1b[2~"), true
	}
	return nil, false
}

// kittyScrollDelta reports scroll amount for kitty CSI-u keys in raw input.
func kittyScrollDelta(code, mods, viewHeight int) (delta int, ok bool) {
	m := mods - 1
	if m < 0 {
		m = 0
	}
	shift := m&1 != 0
	switch code {
	case kittyKeyPageUp, kittyKeyKPPageUp:
		return viewHeight, true
	case kittyKeyPageDown, kittyKeyKPPageDn:
		return -viewHeight, true
	case kittyKeyUp, kittyKeyKPUp:
		if shift {
			return 3, true
		}
	case kittyKeyDown, kittyKeyKPDown:
		if shift {
			return -3, true
		}
	}
	return 0, false
}

// parseScrollInputRaw detects scroll gestures in raw terminal input (before or
// after kitty decode). Handles legacy CSI, kitty CSI-u, and SGR mouse wheel.
func parseScrollInputRaw(data []byte, viewHeight int) (delta int, ok bool) {
	if d, ok := parseScrollInput(data, viewHeight); ok {
		return d, true
	}
	for i := 0; i < len(data); i++ {
		if data[i] != 27 || i+1 >= len(data) || data[i+1] != '[' {
			continue
		}
		code, mods, _, final, n := parseKittyKey(data[i:])
		if n == 0 || final != 'u' {
			continue
		}
		if d, ok := kittyScrollDelta(code, mods, viewHeight); ok {
			return d, true
		}
	}
	return 0, false
}