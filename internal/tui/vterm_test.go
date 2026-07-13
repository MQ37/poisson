package tui

import "regexp"

// vterm is a minimal terminal emulator: it only understands the escape
// sequences this renderer actually emits (cup, clearLine, colors it ignores),
// enough to answer "what does row N actually show right now" after replaying
// a sequence of writes exactly as a real terminal would. Content is kept as
// []byte, not string — string(someByte) converts the byte as a Unicode code
// point, not a raw byte, which mangles multi-byte UTF-8 runes like the dash
// separator glyph.
type vterm struct {
	rows [][]byte
}

func newVterm(height int) *vterm {
	return &vterm{rows: make([][]byte, height+1)} // 1-based
}

var vtermCupRe = regexp.MustCompile(`\x1b\[(\d+);(\d+)H`)

// apply replays one paint()'s worth of raw bytes onto the virtual screen.
func (v *vterm) apply(raw string) {
	row := 0
	i := 0
	for i < len(raw) {
		if loc := vtermCupRe.FindStringSubmatchIndex(raw[i:]); loc != nil && loc[0] == 0 {
			r := 0
			for _, c := range raw[i+loc[2] : i+loc[3]] {
				r = r*10 + int(c-'0')
			}
			row = r
			i += loc[1]
			continue
		}
		if i+4 <= len(raw) && raw[i:i+4] == "\x1b[2K" {
			if row >= 1 && row < len(v.rows) {
				v.rows[row] = nil
			}
			i += 4
			continue
		}
		// An OSC sequence (ESC ']', e.g. the OSC 52 clipboard write) is
		// terminated by BEL (\a) or ST (ESC '\\'), NEVER by a CSI-style final
		// byte in 0x40-0x7e — treating it like a CSI sequence (below) would
		// scan right past its own terminator into whatever comes next
		// (typically the following paint's real cup()+text bytes), silently
		// swallowing real screen content until some unrelated byte happened
		// to land in the CSI final-byte range. A real terminal recognizes OSC
		// framing explicitly, so the emulator must too, or it misrepresents
		// what a real terminal would show after any code path that writes an
		// OSC sequence outside the normal paint() buffer (e.g. copySelectionLocked's
		// direct t.writeRaw(formatOsc52(...))).
		if raw[i] == 0x1b && i+1 < len(raw) && raw[i+1] == ']' {
			j := i + 2
			for j < len(raw) && raw[j] != 0x07 && !(raw[j] == 0x1b && j+1 < len(raw) && raw[j+1] == '\\') {
				j++
			}
			if j < len(raw) {
				if raw[j] == 0x07 {
					j++
				} else {
					j += 2
				}
			}
			i = j
			continue
		}
		// Any other CSI escape sequence (SGR color codes, hide cursor, etc.)
		// is styling only in this renderer, never affecting which row/col is
		// being written, so it's safe to drop for this plain-text emulation.
		// CSI is ESC '[' <params: 0x30-0x3F> <final: 0x40-0x7E>; the '['
		// must be skipped explicitly before scanning for the final byte, or
		// it gets mistaken for one (its own byte value also falls in that
		// range).
		if raw[i] == 0x1b {
			j := i + 1
			if j < len(raw) && raw[j] == '[' {
				j++
			}
			for j < len(raw) && !isFinalByte(raw[j]) {
				j++
			}
			if j < len(raw) {
				j++
			}
			i = j
			continue
		}
		if row >= 1 && row < len(v.rows) {
			v.rows[row] = append(v.rows[row], raw[i])
		}
		i++
	}
}

// isFinalByte reports whether b terminates a CSI escape sequence (a letter,
// not '[' or a parameter digit/semicolon).
func isFinalByte(b byte) bool {
	return b >= 0x40 && b <= 0x7e
}

// visibleRow returns what row (1-based) currently shows, ANSI already gone.
func (v *vterm) visibleRow(row int) string {
	if row < 1 || row >= len(v.rows) {
		return ""
	}
	return string(v.rows[row])
}
