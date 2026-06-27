package tui

// MouseEvent is a parsed SGR mouse report (DECSET 1006).
type MouseEvent struct {
	Button int  // 0=left, 1=middle, 2=right, 64=wheel up, 65=wheel down
	Col    int  // 1-based terminal column
	Row    int  // 1-based terminal row
	Press  bool // true on M (press), false on m (release)
}

// parseMouseEvents extracts all SGR mouse sequences from data.
func parseMouseEvents(data []byte) []MouseEvent {
	var out []MouseEvent
	i := 0
	for i < len(data) {
		if i+6 >= len(data) || data[i] != 27 || data[i+1] != '[' || data[i+2] != '<' {
			i++
			continue
		}
		j := i + 3
		btn := readMouseInt(data, &j)
		if j >= len(data) || data[j] != ';' {
			i++
			continue
		}
		j++
		col := readMouseInt(data, &j)
		if j >= len(data) || data[j] != ';' {
			i++
			continue
		}
		j++
		row := readMouseInt(data, &j)
		if j >= len(data) {
			i++
			continue
		}
		final := data[j]
		if final != 'M' && final != 'm' {
			i++
			continue
		}
		out = append(out, MouseEvent{
			Button: btn,
			Col:    col,
			Row:    row,
			Press:  final == 'M',
		})
		i = j + 1
	}
	return out
}

func readMouseInt(data []byte, i *int) int {
	n := 0
	for *i < len(data) && data[*i] >= '0' && data[*i] <= '9' {
		n = n*10 + int(data[*i]-'0')
		*i++
	}
	return n
}

// mouseWheelDelta maps wheel button codes to scroll delta.
func mouseWheelDelta(btn int) (delta int, ok bool) {
	switch btn {
	case 64:
		return 3, true
	case 65:
		return -3, true
	default:
		return 0, false
	}
}

// dataIsOnlyMouse reports whether data contains only mouse events (no other input).
func dataIsOnlyMouse(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	rest := data
	for {
		events := parseMouseEvents(rest)
		if len(events) == 0 {
			return false
		}
		adv := advancePastMouse(rest)
		if adv <= 0 {
			return false
		}
		rest = rest[adv:]
		if len(rest) == 0 {
			return true
		}
	}
}

func advancePastMouse(data []byte) int {
	for i := 0; i < len(data); i++ {
		if i+6 >= len(data) || data[i] != 27 || data[i+1] != '[' || data[i+2] != '<' {
			continue
		}
		j := i + 3
		readMouseInt(data, &j)
		if j >= len(data) || data[j] != ';' {
			continue
		}
		j++
		readMouseInt(data, &j)
		if j >= len(data) || data[j] != ';' {
			continue
		}
		j++
		readMouseInt(data, &j)
		if j >= len(data) {
			continue
		}
		if data[j] == 'M' || data[j] == 'm' {
			return j + 1
		}
	}
	return 0
}