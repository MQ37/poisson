package tui

// ANSI escape sequence helpers. We avoid bringing in a styling dep.
// Color and basic style sequences are variables populated by theme.go
// (light/dark + 16/truecolor detection from COLORTERM/TERM).
// The non-themable terminal control sequences remain const.

var (
	reset     string
	bold      string
	dim       string
	italic    string
	underline string

	fgBlack   string
	fgRed     string
	fgGreen   string
	fgYellow  string
	fgBlue    string
	fgMagenta string
	fgCyan    string
	fgGray    string

	bgBlack   string
	bgDarkRed string
	bgYellow  string
	bgBlue    string
	bgMagenta string

	// Diff line backgrounds for borderless edit/write rendering.
	bgDiffAdd string
	bgDiffDel string
)

// Cursor / screen control.
const (
	hideCursor   = "\x1b[?25l"
	showCursor   = "\x1b[?25h"
	altScreenOn  = "\x1b[?1049h"
	altScreenOff = "\x1b[?1049l"
	bracketedOn  = "\x1b[?2004h"
	bracketedOff = "\x1b[?2004l"

	// Kitty keyboard protocol: makes the terminal send distinguishable
	// sequences for modifier keys (Shift+Enter → ESC[13;2u instead of \r).
	// Terminals that don't support it ignore these sequences — Shift+Enter
	// then falls back to \r which submits like plain Enter.
	kittyKbOn  = "\x1b[>1u"
	kittyKbOff = "\x1b[<u"

	// SGR mouse tracking: 1000 (clicks/wheel) + 1002 (motion while a button is
	// held, for drag text-selection) + 1006 (extended coordinates).
	mouseOn  = "\x1b[?1000h\x1b[?1002h\x1b[?1006h"
	mouseOff = "\x1b[?1006l\x1b[?1002l\x1b[?1000l"
)

// cup positions the cursor at row (1-based), col (1-based).
func cup(row, col int) string {
	return "\x1b[" + itoa(row) + ";" + itoa(col) + "H"
}

// clearLine erases the current line entirely.
func clearLine() string { return "\x1b[2K" }

// itoa is stdlib-free formatting for small positive integers — we only call
// it from cup/clear builds, never hot-path.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
