package tui

// borderRunes is every box-drawing glyph this codebase's box renderers emit:
// fenced code blocks and <render> citations (highlight.go boxTop/boxSide/
// boxBottom), GFM tables (markdown_table.go), and the diff tool gutter
// (tool_diff.go). U+2500 block only — never collides with real code/prose,
// since ASCII '|' (U+007C, a pipe a shell command might legitimately contain)
// is a different codepoint from '│' (U+2502) used for these borders.
const borderRunes = "╭╮╰╯┌┐└┘├┤┬┴┼─│"

func isBorderRune(r rune) bool {
	for _, b := range borderRunes {
		if r == b {
			return true
		}
	}
	return false
}

// rowContentBounds classifies one already ANSI-stripped screen row and
// returns the span of it worth copying. drop=true means the whole row is
// decorative chrome contributing nothing to a text selection — a table's
// separator/border row, or a code-block/citation fence's top or bottom edge
// (which also carries the language/path label, itself metadata about the
// block rather than body text).
func rowContentBounds(runes []rune) (start, end int, drop bool) {
	n := len(runes)
	if n == 0 {
		return 0, 0, false
	}

	// Pure separator: at least one border glyph present, and every
	// non-space rune is one (table "├──┼──┤"/"╰──┴──╯" rows, any all-dash
	// divider). The "at least one" guard matters: a row of nothing but
	// spaces (e.g. the blank line renderDiffLines inserts between edit
	// hunks) would otherwise vacuously pass "no disqualifying rune found"
	// and get dropped as if it were chrome, silently swallowing real
	// spacing from a copied diff.
	hasBorder, allBorder := false, true
	for _, r := range runes {
		switch {
		case r == ' ':
		case isBorderRune(r):
			hasBorder = true
		default:
			allBorder = false
		}
	}
	if hasBorder && allBorder {
		return 0, 0, true
	}

	// Fence row: code-block/citation top or bottom edge, e.g.
	// "╭─ python ────╮" or "╰──────────╯" — dropped whole, label included,
	// since the label is block metadata, not content to paste elsewhere.
	if (runes[0] == '╭' && runes[n-1] == '╮') || (runes[0] == '╰' && runes[n-1] == '╯') {
		return 0, 0, true
	}

	// Bordered content row: code-block side or table row, "│ code │" /
	// "│ cell │ cell │". Trim exactly the border + its one padding space at
	// each edge that's actually present — never more, so a genuine leading/
	// trailing content space inside the box isn't eaten.
	start, end = 0, n
	if runes[0] == '│' {
		start = 1
		if start < n && runes[start] == ' ' {
			start++
		}
	}
	if runes[n-1] == '│' {
		end = n - 1
		if end > start && runes[end-1] == ' ' {
			end--
		}
	}
	return start, end, false
}

// stripInteriorBorders replaces any '│' left inside an already-clipped
// selection substring (a table's interior column separators — the edge
// case rowContentBounds' start/end trim doesn't reach) with two spaces, so
// copied table cells read as plain space-separated text instead of running
// together or keeping a stray box glyph.
func stripInteriorBorders(runes []rune) []rune {
	out := make([]rune, 0, len(runes))
	for _, r := range runes {
		if r == '│' {
			out = append(out, ' ', ' ')
			continue
		}
		out = append(out, r)
	}
	return out
}
