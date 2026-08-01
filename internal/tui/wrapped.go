package tui

import (
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

// runeDisplayWidth is the terminal column width of r: 0 for zero-width
// combining marks, 1 for ordinary/narrow runes, 2 for wide runes (CJK
// Unified Ideographs, most emoji, fullwidth forms). Every wrap/cursor
// calculation in this file — and visibleWidth/truncateToWidth in
// scrollback.go — is defined in terms of display columns, not rune counts:
// a naive 1-rune=1-column assumption under-counts any wide rune by half,
// letting the real terminal auto-wrap mid-row on content this code still
// believes fits, corrupting every subsequent absolute-cursor-addressed
// write on screen for the rest of that frame.
func runeDisplayWidth(r rune) int {
	w := runewidth.RuneWidth(r)
	if w < 0 {
		return 0
	}
	return w
}

// stringDisplayWidth sums runeDisplayWidth over s.
func stringDisplayWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeDisplayWidth(r)
	}
	return w
}

// runesDisplayWidth sums runeDisplayWidth over an already-decoded rune
// slice — avoids a string(...) allocation in the editor cursor-math hot
// path below, where callers already hold rune slices.
func runesDisplayWidth(runes []rune) int {
	w := 0
	for _, r := range runes {
		w += runeDisplayWidth(r)
	}
	return w
}

// wrapChunk is one wrapped screen row of a logical line: just its rune
// length, since the caller already has (or can slice) the original runes.
type wrapChunk struct {
	runeLen int
}

// wrapChunks is the single shared wrap-boundary walk every rune-index <->
// (screen row, screen column) conversion in this file is built on, so the
// boundary decision can never drift between wrapOne (rendering) and
// screenCursor/screenToLogical (cursor placement) — both call this instead
// of re-deriving where a line breaks.
//
// Hard wrapping: break as soon as the next rune would exceed width display
// columns, never mid-rune (a rune is atomic — a wide rune that only
// partially fits in the remaining columns moves whole to the next chunk,
// the same "early auto-wrap before a wide glyph" behavior a real terminal
// itself exhibits, rather than splitting it, which isn't representable).
func wrapChunks(line string, width int) []wrapChunk {
	if width < 1 {
		width = 1
	}
	runes := []rune(line)
	if len(runes) == 0 {
		return []wrapChunk{{runeLen: 0}}
	}
	var chunks []wrapChunk
	start := 0
	col := 0
	for i, r := range runes {
		w := runeDisplayWidth(r)
		if col+w > width && i > start {
			chunks = append(chunks, wrapChunk{runeLen: i - start})
			start = i
			col = 0
		}
		col += w
	}
	chunks = append(chunks, wrapChunk{runeLen: len(runes) - start})
	return chunks
}

// wrapLines splits each logical line into wrapped chunks whose display
// width is at most `width` columns. The result is a flat list of display
// strings — one per screen row.
func wrapLines(logical []string, width int) []string {
	if width < 1 {
		width = 1
	}
	var out []string
	for _, line := range logical {
		out = append(out, wrapOne(line, width)...)
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}

// wrapOne hard-wraps a single logical line into chunks of at most width
// display columns each (see wrapChunks).
func wrapOne(line string, width int) []string {
	runes := []rune(line)
	chunks := wrapChunks(line, width)
	out := make([]string, len(chunks))
	pos := 0
	for i, c := range chunks {
		out[i] = string(runes[pos : pos+c.runeLen])
		pos += c.runeLen
	}
	return out
}

// screenCursor returns the (screenRow, screenCol) for the editor's current
// (row, col) — col is a RUNE index into e.lines[e.row], screenCol is a
// display COLUMN within its wrapped row.
func screenCursor(e *editor, width int) (int, int) {
	if width < 1 {
		width = 1
	}
	rowOffset := 0
	for r := 0; r < e.row; r++ {
		rowOffset += visualLineCount(e.lines[r], width)
	}
	line := e.lines[e.row]
	runes := []rune(line)
	col := e.col
	if col > len(runes) {
		col = len(runes)
	}
	if col < 0 {
		col = 0
	}
	chunks := wrapChunks(line, width)
	pos := 0
	for ci, c := range chunks {
		end := pos + c.runeLen
		if col == end && col > 0 && ci+1 < len(chunks) {
			// Cursor sits exactly on an internal chunk boundary (more
			// text follows in the next chunk): stays at the END of
			// THIS row rather than the start of a phantom/next one —
			// verbatim behavior carried over from the pre-width-aware
			// implementation (see its own comment on why the line's
			// own end must land at the end of the last row, not a
			// phantom row below it; this fix only changes how a
			// display column is computed, not that placement rule).
			return rowOffset + ci, runesDisplayWidth(runes[pos:end])
		}
		if col <= end {
			return rowOffset + ci, runesDisplayWidth(runes[pos:col])
		}
		pos = end
	}
	// Unreachable given col is clamped to len(runes) above, but stay safe.
	last := len(chunks) - 1
	return rowOffset + last, runesDisplayWidth(runes[pos:])
}

// visualLineCount returns how many screen rows a single logical line takes.
func visualLineCount(line string, width int) int {
	return len(wrapChunks(line, width))
}

// totalVisualLines is the total screen rows occupied by all logical lines.
func totalVisualLines(e *editor, width int) int {
	if width < 1 {
		width = 1
	}
	if len(e.lines) == 0 {
		return 1
	}
	total := 0
	for _, line := range e.lines {
		total += visualLineCount(line, width)
	}
	if total == 0 {
		total = 1
	}
	return total
}

// screenToLogical maps a (visualRow, visualCol) back to (row, col) within the
// editor. Used when handling Up/Down from the screen grid. visualCol is a
// display column; the returned col is a rune index — when visualCol lands
// inside a wide rune's 2-column footprint, it resolves to that rune's own
// (pre-rune) index, same as landing exactly on its first column.
func screenToLogical(e *editor, width, visualRow, visualCol int) (int, int) {
	if width < 1 {
		width = 1
	}
	cur := 0
	for r, line := range e.lines {
		chunks := wrapChunks(line, width)
		n := len(chunks)
		if visualRow < cur+n {
			local := visualRow - cur
			runes := []rune(line)
			pos := 0
			for i := 0; i < local; i++ {
				pos += chunks[i].runeLen
			}
			chunkRunes := runes[pos : pos+chunks[local].runeLen]
			target := visualCol
			if target < 0 {
				target = 0
			}
			col := pos
			acc := 0
			for _, cr := range chunkRunes {
				if acc >= target {
					break
				}
				acc += runeDisplayWidth(cr)
				col++
			}
			max := len(runes)
			if col > max {
				col = max
			}
			if col < 0 {
				col = 0
			}
			return r, col
		}
		cur += n
	}
	last := len(e.lines) - 1
	if last < 0 {
		return 0, 0
	}
	return last, utf8.RuneCountInString(e.lines[last])
}
