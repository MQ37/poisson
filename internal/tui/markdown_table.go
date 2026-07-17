package tui

import (
	"strings"
	"unicode/utf8"
)

// isTableRow reports whether a line looks like a markdown table row.
func isTableRow(line string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.Contains(trimmed, "|") {
		return false
	}
	cells := splitTableCells(trimmed)
	return len(cells) >= 2
}

func isTableSeparator(line string) bool {
	cells := splitTableCells(strings.TrimSpace(line))
	if len(cells) < 2 {
		return false
	}
	for _, c := range cells {
		c = strings.TrimSpace(c)
		if c == "" {
			return false
		}
		for _, r := range c {
			if r != '-' && r != ':' && r != ' ' {
				return false
			}
		}
	}
	return true
}

func splitTableCells(line string) []string {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "|") {
		trimmed = strings.TrimPrefix(trimmed, "|")
	}
	if strings.HasSuffix(trimmed, "|") {
		trimmed = strings.TrimSuffix(trimmed, "|")
	}
	parts := strings.Split(trimmed, "|")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

func tableBlockEnd(lines []string, start int) int {
	if start >= len(lines) || !isTableRow(lines[start]) {
		return start
	}
	// GFM tables require a separator row immediately after the header.
	if start+1 >= len(lines) || !isTableSeparator(lines[start+1]) {
		return start
	}
	end := start + 2
	for end < len(lines) && isTableRow(lines[end]) && !isTableSeparator(lines[end]) {
		end++
	}
	if end-start < 2 {
		return start
	}
	return end
}

// renderMarkdownTable renders a GFM table block as a bordered ANSI box.
func renderMarkdownTable(lines []string, width int, basePrefix string) []string {
	if width < 12 {
		width = 12
	}
	var rows [][]string
	for _, ln := range lines {
		if isTableSeparator(ln) {
			continue
		}
		rows = append(rows, splitTableCells(ln))
	}
	if len(rows) == 0 {
		return nil
	}
	colCount := len(rows[0])
	for _, r := range rows {
		if len(r) > colCount {
			colCount = len(r)
		}
	}
	for i := range rows {
		for len(rows[i]) < colCount {
			rows[i] = append(rows[i], "")
		}
	}

	widths := tableColumnWidths(rows, colCount)
	widths = fitTableWidths(widths, width)

	var out []string
	prefix := basePrefix
	for i, row := range rows {
		if i == 0 {
			out = append(out, prefix+tableBorder(widths, "╭", "┬", "╮", "─")+reset)
			prefix = ""
		}
		out = append(out, tableDataRows(widths, row, i == 0)...)
		if i == 0 {
			out = append(out, tableBorder(widths, "├", "┼", "┤", "─"))
		}
	}
	out = append(out, tableBorder(widths, "╰", "┴", "╯", "─"))
	return out
}

func tableColumnWidths(rows [][]string, colCount int) []int {
	widths := make([]int, colCount)
	for c := 0; c < colCount; c++ {
		for _, row := range rows {
			w := utf8.RuneCountInString(stripANSI(renderInline(row[c])))
			if w > widths[c] {
				widths[c] = w
			}
		}
		if widths[c] < 1 {
			widths[c] = 1
		}
	}
	return widths
}

// fitTableWidths shrinks columns to fit maxWidth terminal columns.
func fitTableWidths(widths []int, maxWidth int) []int {
	const minCol = 2
	// Each column adds: leading space + content + trailing space + │ = w + 3
	total := tableRowWidth(widths)
	for total > maxWidth {
		idx := maxIntIndex(widths)
		if widths[idx] <= minCol {
			break
		}
		widths[idx]--
		total = tableRowWidth(widths)
	}
	if tableRowWidth(widths) > maxWidth {
		for i := range widths {
			widths[i] = minCol
		}
	}
	return widths
}

func tableRowWidth(widths []int) int {
	n := 1 // closing │
	for _, w := range widths {
		n += w + 3 // │ + space + content + space
	}
	return n
}

func tableBorder(widths []int, left, mid, right, fill string) string {
	var b strings.Builder
	b.WriteString(fgGray)
	b.WriteString(left)
	for i, w := range widths {
		b.WriteString(strings.Repeat(fill, w+2)) // pad spaces inside cells
		if i < len(widths)-1 {
			b.WriteString(mid)
		}
	}
	b.WriteString(right)
	return b.String()
}

// tableDataRows renders one logical table row as one or more physical output
// lines: each cell wraps at its column width (word-wrap, ANSI-aware — same
// wrapANSI used for prose and code blocks) instead of being cut off. The row
// grows to the tallest wrapped cell; shorter cells pad with blank lines. This
// is the only thing standing between a long cell value and truncateToWidth
// silently dropping it — see the directory-listing / long-description table
// bug this fixes.
func tableDataRows(widths []int, cells []string, header bool) []string {
	cellLines := make([][]string, len(widths))
	height := 1
	for c, w := range widths {
		cell := cells[c]
		var styled string
		if header {
			styled = bold + fgCyan + stripANSI(renderInline(cell)) + reset
		} else {
			styled = renderInline(cell)
		}
		lines := wrapANSI(styled, w)
		if len(lines) == 0 {
			lines = []string{""}
		}
		cellLines[c] = lines
		if len(lines) > height {
			height = len(lines)
		}
	}
	out := make([]string, height)
	for r := 0; r < height; r++ {
		var b strings.Builder
		b.WriteString(fgGray + "│" + reset)
		for c, w := range widths {
			var content string
			if r < len(cellLines[c]) {
				content = cellLines[c][r]
			}
			pad := w - visibleWidth(content)
			if pad < 0 {
				content = truncateToWidth(content, w)
				pad = w - visibleWidth(content)
			}
			if pad < 0 {
				pad = 0
			}
			b.WriteString(" ")
			b.WriteString(content)
			b.WriteString(strings.Repeat(" ", pad))
			b.WriteString(" ")
			b.WriteString(fgGray + "│" + reset)
		}
		out[r] = b.String()
	}
	return out
}

func maxIntIndex(xs []int) int {
	best, idx := 0, 0
	for i, x := range xs {
		if x > best {
			best, idx = x, i
		}
	}
	return idx
}
