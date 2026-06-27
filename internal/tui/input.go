package tui

import (
	"strings"
	"unicode/utf8"
)

// editor is the multi-line input buffer. Lines are joined by '\n'. The
// cursor addresses (row, col) in rune coordinates within a logical "lines"
// slice. Render is left to the caller; editor just owns state.
type editor struct {
	lines     []string // row i holds the i-th logical line (no trailing \n)
	row       int      // 0-indexed cursor row
	col       int      // 0-indexed cursor column in runes within lines[row]
	paste     bool     // mid-bracketed-paste
	pasteBuf  []byte
	wrapWidth int // current wrap width in runes; 0 means no wrap
}

func newEditor() *editor {
	return &editor{lines: []string{""}}
}

func (e *editor) text() string { return strings.Join(e.lines, "\n") }

func (e *editor) setText(s string) {
	if s == "" {
		e.lines = []string{""}
		e.row = 0
		e.col = 0
		return
	}
	e.lines = strings.Split(s, "\n")
	e.row = len(e.lines) - 1
	e.col = e.runeCount(e.row)
}

// runeCount is column width in runes for a row.
func (e *editor) runeCount(row int) int {
	return utf8.RuneCountInString(e.lines[row])
}

// clampCursor keeps row/col within bounds (call after mutations).
func (e *editor) clampCursor() {
	if e.row < 0 {
		e.row = 0
	}
	if e.row >= len(e.lines) {
		e.row = len(e.lines) - 1
	}
	if e.row < 0 {
		e.row = 0
	}
	if e.col < 0 {
		e.col = 0
	}
	max := e.runeCount(e.row)
	if e.col > max {
		e.col = max
	}
}

// insertRune inserts a printable rune at the cursor.
func (e *editor) insertRune(r rune) {
	runes := []rune(e.lines[e.row])
	result := make([]rune, 0, len(runes)+1)
	result = append(result, runes[:e.col]...)
	result = append(result, r)
	result = append(result, runes[e.col:]...)
	e.lines[e.row] = string(result)
	e.col++
}

// insertText inserts a (possibly multi-line) string at the cursor.
func (e *editor) insertText(s string) {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	parts := strings.Split(s, "\n")
	if len(parts) == 1 {
		runes := []rune(e.lines[e.row])
		result := make([]rune, 0, len(runes)+len(parts[0]))
		result = append(result, runes[:e.col]...)
		result = append(result, []rune(parts[0])...)
		result = append(result, runes[e.col:]...)
		e.lines[e.row] = string(result)
		e.col += len([]rune(parts[0]))
		return
	}
	// Multi-line: first fragment joins current row, last opens the tail.
	colByte := runeByteIndex(e.lines[e.row], e.col)
	head := e.lines[e.row][:colByte]
	tail := e.lines[e.row][colByte:]
	first := head + parts[0]
	last := parts[len(parts)-1] + tail
	middle := parts[1 : len(parts)-1]
	result := make([]string, 0, len(e.lines)+len(parts)-1)
	result = append(result, e.lines[:e.row]...)
	result = append(result, first)
	result = append(result, middle...)
	result = append(result, last)
	result = append(result, e.lines[e.row+1:]...)
	e.lines = result
	e.row += len(parts) - 1
	e.col = utf8.RuneCountInString(parts[len(parts)-1])
}

// backspace deletes the rune before the cursor; merges with prev line at col=0.
func (e *editor) backspace() {
	if e.col > 0 {
		row := e.lines[e.row]
		idx := runeByteIndex(row, e.col-1)
		next := runeByteIndex(row, e.col)
		e.lines[e.row] = row[:idx] + row[next:]
		e.col--
		return
	}
	if e.row == 0 {
		return
	}
	prev := e.lines[e.row-1]
	cur := e.lines[e.row]
	e.lines[e.row-1] = prev + cur
	e.lines = append(e.lines[:e.row], e.lines[e.row+1:]...)
	e.row--
	e.col = utf8.RuneCountInString(prev)
}

// delete deletes the rune at the cursor; merges next line at end-of-row.
func (e *editor) delete() {
	row := e.lines[e.row]
	if e.col < utf8.RuneCountInString(row) {
		idx := runeByteIndex(row, e.col)
		next := runeByteIndex(row, e.col+1)
		e.lines[e.row] = row[:idx] + row[next:]
		return
	}
	if e.row == len(e.lines)-1 {
		return
	}
	e.lines[e.row] = row + e.lines[e.row+1]
	e.lines = append(e.lines[:e.row+1], e.lines[e.row+2:]...)
}

func (e *editor) moveLeft() {
	if e.col > 0 {
		e.col--
	} else if e.row > 0 {
		e.row--
		e.col = e.runeCount(e.row)
	}
}

func (e *editor) moveRight() {
	if e.col < e.runeCount(e.row) {
		e.col++
	} else if e.row < len(e.lines)-1 {
		e.row++
		e.col = 0
	}
}

func (e *editor) moveUp() {
	if e.row == 0 {
		return
	}
	e.row--
	if e.col > e.runeCount(e.row) {
		e.col = e.runeCount(e.row)
	}
}

func (e *editor) moveDown() {
	if e.row == len(e.lines)-1 {
		return
	}
	e.row++
	if e.col > e.runeCount(e.row) {
		e.col = e.runeCount(e.row)
	}
}

func (e *editor) moveHome() { e.col = 0 }

func (e *editor) moveEnd() { e.col = e.runeCount(e.row) }

// moveUpScreen moves the cursor one screen line up, wrapping across logical
// rows. If the cursor is at the top, it stays put.
func (e *editor) moveUpScreen(width int) {
	sr, sc := screenCursor(e, width)
	if sr == 0 {
		return
	}
	r, c := screenToLogical(e, width, sr-1, sc)
	e.row = r
	e.col = c
}

// moveDownScreen moves the cursor one screen line down. If at the bottom of
// the wrapped grid, the cursor moves to col=len of the last logical row.
func (e *editor) moveDownScreen(width int) {
	sr, sc := screenCursor(e, width)
	last := totalVisualLines(e, width) - 1
	if sr >= last {
		e.row = len(e.lines) - 1
		e.col = e.runeCount(e.row)
		return
	}
	r, c := screenToLogical(e, width, sr+1, sc)
	e.row = r
	e.col = c
}

// moveHomeScreen moves the cursor to the start of the current screen line.
func (e *editor) moveHomeScreen(width int) {
	sr, _ := screenCursor(e, width)
	r, c := screenToLogical(e, width, sr, 0)
	e.row, e.col = r, c
}

// moveEndScreen moves the cursor to the end of the current screen line.
// With hard character wrapping, the end is the lesser of (start+width) and
// the line's rune count.
func (e *editor) moveEndScreen(width int) {
	sr, _ := screenCursor(e, width)
	r, c := screenToLogical(e, width, sr, 0)
	lineRunes := e.runeCount(r)
	end := c + width
	if end > lineRunes {
		end = lineRunes
	}
	e.row, e.col = r, end
}

// insertNewline splits the row at the cursor, creating a new logical line
// from the text after the cursor.
func (e *editor) insertNewline() {
	row := e.lines[e.row]
	idx := runeByteIndex(row, e.col)
	left, right := row[:idx], row[idx:]
	// Build a new slice: lines[:row] + left + right + lines[row+1:]
	newLines := make([]string, 0, len(e.lines)+1)
	newLines = append(newLines, e.lines[:e.row]...)
	newLines = append(newLines, left, right)
	newLines = append(newLines, e.lines[e.row+1:]...)
	e.lines = newLines
	e.row++
	e.col = 0
}

// decodeKittyKeys translates kitty-keyboard-protocol CSI-u sequences into the
// raw byte equivalents the rest of the input pipeline already understands.
// Non-'u' sequences (arrows, Home/End, bracketed-paste markers) pass through
// unchanged. This lets us enable the kitty protocol — so Shift+Enter is
// distinguishable from Enter — without teaching every key handler the CSI-u
// encoding. Shift+Enter maps to '\n' (newline, same as Ctrl+J); plain Enter to
// '\r' (submit); Ctrl+<letter> to its C0 control byte; Backspace to 0x7f.
func decodeKittyKeys(data []byte) []byte {
	if indexOf(data, []byte{27, '['}) < 0 {
		return data // no CSI at all — fast path
	}
	out := make([]byte, 0, len(data))
	i := 0
	for i < len(data) {
		if data[i] == 27 && i+1 < len(data) && data[i+1] == '[' {
			// Bracketed paste markers must survive verbatim for the editor.
			if hasPrefix(data[i:], pasteStartV2) || hasPrefix(data[i:], pasteEndV2) {
				out = append(out, data[i:i+len(pasteStartV2)]...)
				i += len(pasteStartV2)
				continue
			}
			if code, mods, final, n := parseKittyKey(data[i:]); n > 0 && final == 'u' {
				if b, ok := kittyToLegacyCSI(code, mods); ok {
					out = append(out, b...)
				} else if b, ok := kittyToBytes(code, mods); ok {
					out = append(out, b...)
				}
				i += n
				continue
			}
			// Other CSI sequence (arrows, etc.) — copy verbatim to its final byte.
			j := i + 2
			for j < len(data) && (data[j] < 0x40 || data[j] > 0x7e) {
				j++
			}
			if j < len(data) {
				j++
			}
			out = append(out, data[i:j]...)
			i = j
			continue
		}
		out = append(out, data[i])
		i++
	}
	return out
}

// kittyToBytes maps a kitty key (code, mods) to the raw bytes the editor
// expects. ok is false for keys with no raw equivalent (they're dropped).
func kittyToBytes(code, mods int) ([]byte, bool) {
	m := mods - 1
	if m < 0 {
		m = 0
	}
	shift := m&1 != 0
	ctrl := m&4 != 0
	switch code {
	case 13: // Enter / Return
		if shift {
			return []byte{'\n'}, true // Shift+Enter → newline (like Ctrl+J)
		}
		return []byte{'\r'}, true // Enter → submit
	case 8, 127: // Backspace
		return []byte{127}, true
	case 9: // Tab
		return []byte{9}, true
	case 27: // Escape
		return []byte{27}, true
	case kittyKeyEscape:
		return []byte{27}, true
	case kittyKeyEnter:
		if shift {
			return []byte{'\n'}, true
		}
		return []byte{'\r'}, true
	case kittyKeyTab:
		return []byte{9}, true
	case kittyKeyBackspace:
		return []byte{127}, true
	}
	if ctrl && ((code >= 'a' && code <= 'z') || (code >= 'A' && code <= 'Z')) {
		return []byte{byte(code) & 0x1f}, true
	}
	// Kitty functional keys live in the PUA range — never treat as text.
	if code >= 57344 && code < 63744 {
		return nil, false
	}
	if !ctrl && code >= 32 && code != 127 {
		return []byte(string(rune(code))), true
	}
	return nil, false
}

// runeByteIndex converts a rune column to a byte index in s. col is clamped
// to [0, runeCount(s)].
func runeByteIndex(s string, col int) int {
	if col <= 0 {
		return 0
	}
	for i := 0; i < len(s); {
		if col == 0 {
			return i
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
		col--
	}
	return len(s)
}

// pasteEndV2 markers for bracketed paste mode (v2 editor). The classic readline
// editor keeps its own copies in tui.go to avoid coupling the two paths.
var (
	pasteStartV2 = []byte("\x1b[200~")
	pasteEndV2   = []byte("\x1b[201~")
)

// feed appends raw input bytes to the editor. Returns (submitted string, ok).
// submitted is non-empty when the user pressed Ctrl+J or Esc+Enter; the
// caller should dispatch it to the agent.
func (e *editor) feed(data []byte) (string, bool) {
	if e.paste {
		if idx := indexOf(data, pasteEndV2); idx >= 0 {
			e.pasteBuf = append(e.pasteBuf, data[:idx]...)
			e.insertText(string(e.pasteBuf))
			e.paste = false
			e.pasteBuf = nil
			return e.feed(data[idx+len(pasteEndV2):])
		}
		e.pasteBuf = append(e.pasteBuf, data...)
		return "", false
	}

	i := 0
	for i < len(data) {
		b := data[i]
		switch {
		case b == 13: // Enter (CR) → submit
			return e.text(), false

		case b == 10: // Ctrl+J / LF → newline
			e.insertNewline()
			i++

		case b == 11: // Ctrl+K — delete to end of row
			e.lines[e.row] = runeSubstring(e.lines[e.row], 0, e.col)
			i++

		case b == 21: // Ctrl+U — delete to start of row
			tail := runeSubstring(e.lines[e.row], e.col, e.runeCount(e.row))
			e.lines[e.row] = tail
			e.col = 0
			i++

		case b == 23: // Ctrl+W — delete word backward
			e.deleteWordBackward()
			i++

		case b == 1: // Ctrl+A — Home (screen line)
			e.moveHomeScreen(e.wrapWidth)
			i++

		case b == 5: // Ctrl+E — End (screen line)
			e.moveEndScreen(e.wrapWidth)
			i++

		case b == 4: // Ctrl+D — EOF; treat as delete when buffer non-empty
			if e.text() != "" {
				e.delete()
			} else {
				return "", true // signal quit (empty buffer + Ctrl+D)
			}
			i++

		case b == 8 || b == 127: // Backspace / Ctrl-H
			e.backspace()
			i++

		case b == 27: // ESC — sequence
			consumed, submitted := e.handleEscape(data[i:])
			if submitted {
				return e.text(), false
			}
			i += consumed
			if i == 0 {
				i = 1
			}

		case b < 32: // other control — ignore
			i++

		default:
			r, size := utf8.DecodeRune(data[i:])
			if r == utf8.RuneError && size <= 1 {
				i++
				continue
			}
			e.insertRune(r)
			i += size
		}
	}
	return "", false
}

// handleEscape parses a CSI / SS3 / OSC sequence starting at data[0]==ESC.
// Returns bytes consumed and whether the caller should submit (Esc+Enter).
func (e *editor) handleEscape(data []byte) (int, bool) {
	if len(data) < 2 {
		return 1, false
	}
	if data[1] == '[' {
		if hasPrefix(data, pasteStartV2) {
			rest := data[len(pasteStartV2):]
			if idx := indexOf(rest, pasteEndV2); idx >= 0 {
				e.insertText(string(rest[:idx]))
				return len(pasteStartV2) + idx + len(pasteEndV2), false
			}
			e.paste = true
			e.pasteBuf = append([]byte{}, rest...)
			return len(data), false
		}
		// Legacy CSI function keys (must precede parseKittyKey — it also
		// matches ESC [ <n> ~ sequences and would swallow Delete/Insert).
		if len(data) >= 4 && data[2] == '3' && data[3] == '~' {
			e.delete()
			return 4, false
		}
		if len(data) >= 4 && data[2] == '2' && data[3] == '~' {
			return 4, false // Insert — no-op in line editor
		}
		// Kitty keyboard protocol: ESC [ <code> ; <mods> <final>
		// Shift+Enter: code=13, mods=2, final='u' or '~'
		// Enter with no mods: code=13, mods=1 or absent, final='u'
		if code, mods, final, n := parseKittyKey(data); n > 0 && (final == 'u' || final == '~') {
			if code == 13 {
				if mods == 2 {
					e.insertNewline()
					return n, false
				}
				return n, true // submit
			}
			return n, false
		}
		// Legacy CSI: arrow keys, Home/End, etc.
		if len(data) >= 3 {
			switch data[2] {
			case 'A':
				e.moveUpScreen(e.wrapWidth)
				return 3, false
			case 'B':
				e.moveDownScreen(e.wrapWidth)
				return 3, false
			case 'C':
				e.moveRight()
				return 3, false
			case 'D':
				e.moveLeft()
				return 3, false
			case 'H':
				e.moveHomeScreen(e.wrapWidth)
				return 3, false
			case 'F':
				e.moveEndScreen(e.wrapWidth)
				return 3, false
			}
		}
		// Walk to final byte.
		j := 2
		for j < len(data) && (data[j] < 0x40 || data[j] > 0x7e) {
			j++
		}
		if j < len(data) {
			j++
		}
		return j, false
	}
	// ESC\r (Esc+Enter) → submit
	if data[1] == '\r' || data[1] == 13 {
		return 2, true
	}
	if data[1] == 127 || data[1] == 8 {
		e.deleteWordBackward()
		return 2, false
	}
	return 2, false
}

// parseKittyKey parses a CSI sequence of the form
// ESC [ <code> [ ; <mod1> [ ; <mod2> ] ] <final>.
// It handles both the kitty keyboard protocol (ESC[13;2u) and the xterm
// modifyOtherKeys encoding (ESC[27;2;13~) for Shift+Enter.
// Returns the effective key code, modifier bits, final byte, and total bytes
// consumed. Returns n==0 if data is not a recognized key sequence.
func parseKittyKey(data []byte) (code, mods int, final byte, n int) {
	if len(data) < 4 || data[0] != 27 || data[1] != '[' {
		return 0, 0, 0, 0
	}
	i := 2
	fields := []int{}
	for i < len(data) {
		val := 0
		hadDigit := false
		for i < len(data) && data[i] >= '0' && data[i] <= '9' {
			val = val*10 + int(data[i]-'0')
			i++
			hadDigit = true
		}
		fields = append(fields, val)
		if i >= len(data) {
			return 0, 0, 0, 0
		}
		if data[i] == ';' {
			i++
			if i >= len(data) {
				return 0, 0, 0, 0
			}
			if !hadDigit && len(fields) > 0 {
				// empty field — treat as 0
			}
			continue
		}
		break
	}
	if i >= len(data) {
		return 0, 0, 0, 0
	}
	final = data[i]
	if final < 0x40 || final > 0x7e {
		return 0, 0, 0, 0
	}
	n = i + 1

	// Determine the effective key code and modifier bits.
	switch len(fields) {
	case 0:
		return 0, 1, final, n
	case 1:
		return fields[0], 1, final, n
	case 2:
		// kitty: <keycode> ; <mods>
		mods := fields[1]
		if mods == 0 {
			mods = 1
		}
		return fields[0], mods, final, n
	default:
		// xterm modifyOtherKeys: <keycode> ; <mods> ; <actualKey>
		// e.g. ESC[27;2;13~ means Shift+Enter (code 27, mods 2, actual key 13).
		mods := fields[1]
		if mods == 0 {
			mods = 1
		}
		return fields[2], mods, final, n
	}
}

func (e *editor) deleteWordBackward() {
	if e.col == 0 && e.row == 0 {
		return
	}
	row := e.lines[e.row]
	runes := []rune(row)
	col := e.col
	// Skip trailing whitespace.
	for col > 0 && (runes[col-1] == ' ' || runes[col-1] == '\t') {
		col--
	}
	for col > 0 && runes[col-1] != ' ' && runes[col-1] != '\t' {
		col--
	}
	e.lines[e.row] = string(runes[:col]) + string(runes[e.col:])
	e.col = col
	if e.col == 0 && e.row > 0 {
		// Merge with previous line.
		prev := e.lines[e.row-1]
		e.lines[e.row-1] = prev + e.lines[e.row]
		e.lines = append(e.lines[:e.row], e.lines[e.row+1:]...)
		e.row--
		e.col = utf8.RuneCountInString(prev)
	}
}

// runeSubstring returns the rune slice [from, to) of s.
func runeSubstring(s string, from, to int) string {
	runes := []rune(s)
	if from < 0 {
		from = 0
	}
	if to > len(runes) {
		to = len(runes)
	}
	return string(runes[from:to])
}

// indexOf is bytes.Index but small/inline; mirrors tui.go's helper.
func indexOf(s, sep []byte) int {
	if len(sep) == 0 {
		return 0
	}
	for i := 0; i+len(sep) <= len(s); i++ {
		for j := 0; j < len(sep); j++ {
			if s[i+j] != sep[j] {
				goto next
			}
		}
		return i
	next:
	}
	return -1
}

func hasPrefix(s, prefix []byte) bool {
	if len(s) < len(prefix) {
		return false
	}
	for i := range prefix {
		if s[i] != prefix[i] {
			return false
		}
	}
	return true
}
