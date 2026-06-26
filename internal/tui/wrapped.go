package tui

import (
	"unicode/utf8"
)

// wrapLines splits each logical line into wrapped chunks of at most `width`
// runes. The result is a flat list of display strings — one per screen row.
//
// We use hard character wrapping (break at exactly `width` runes) rather than
// word-wrap. This keeps the cursor math trivial: chunk i has exactly `width`
// runes (except the last), so screenRow = col / width and screenCol = col % width.
// No whitespace stripping means chunk lengths always sum to the original line
// length — no cursor desync across wrap boundaries.
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

// wrapOne hard-wraps a single logical line to width runes per chunk.
func wrapOne(line string, width int) []string {
	if width < 1 {
		width = 1
	}
	runes := []rune(line)
	if len(runes) <= width {
		return []string{line}
	}
	var out []string
	for len(runes) > width {
		out = append(out, string(runes[:width]))
		runes = runes[width:]
	}
	if len(runes) > 0 {
		out = append(out, string(runes))
	}
	return out
}

// screenCursor returns the (screenRow, screenCol) for the editor's current
// (row, col). With hard character wrapping, the math is trivial:
//
//	screenRow = rowOffset + col / width
//	screenCol = col % width
//
// where rowOffset is the total screen rows of all logical lines before `row`.
func screenCursor(e *editor, width int) (int, int) {
	if width < 1 {
		width = 1
	}
	rowOffset := 0
	for r := 0; r < e.row; r++ {
		rowOffset += visualLineCount(e.lines[r], width)
	}
	runes := []rune(e.lines[e.row])
	col := e.col
	if col > len(runes) {
		col = len(runes)
	}
	if col < 0 {
		col = 0
	}
	// At the end of a line whose length is an exact multiple of width, the
	// cursor belongs at the END of the last wrapped row (col=width), not the
	// start of a phantom next row. Without this the caret detaches onto a
	// blank row below the text.
	if col > 0 && col%width == 0 {
		return rowOffset + col/width - 1, width
	}
	return rowOffset + col/width, col % width
}

// visualLineCount returns how many screen rows a single logical line takes.
func visualLineCount(line string, width int) int {
	if width < 1 {
		width = 1
	}
	n := utf8.RuneCountInString(line)
	if n == 0 {
		return 1
	}
	return (n + width - 1) / width // ceil(n / width)
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
// editor. Used when handling Up/Down from the screen grid.
//
// With hard character wrapping, the inverse is:
//
//	col = (visualRow - rowOffset) * width + visualCol
func screenToLogical(e *editor, width, visualRow, visualCol int) (int, int) {
	if width < 1 {
		width = 1
	}
	cur := 0
	for r, line := range e.lines {
		n := visualLineCount(line, width)
		if visualRow < cur+n {
			local := visualRow - cur
			col := local*width + visualCol
			// Clamp to line length.
			max := utf8.RuneCountInString(line)
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
