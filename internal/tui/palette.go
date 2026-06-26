package tui

// ANSI escape sequence helpers. We avoid bringing in a styling dep — these
// constants are the only color codes we use.

const (
	reset     = "\x1b[0m"
	bold      = "\x1b[1m"
	dim       = "\x1b[2m"
	italic    = "\x1b[3m"
	underline = "\x1b[4m"

	fgBlack   = "\x1b[30m"
	fgRed     = "\x1b[31m"
	fgGreen   = "\x1b[32m"
	fgYellow  = "\x1b[33m"
	fgBlue    = "\x1b[34m"
	fgMagenta = "\x1b[35m"
	fgCyan    = "\x1b[36m"
	fgGray    = "\x1b[90m"

	bgBlack   = "\x1b[40m"
	bgDarkRed = "\x1b[41m"
	bgYellow  = "\x1b[43m"
	bgBlue    = "\x1b[44m"
	bgMagenta = "\x1b[45m"
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
