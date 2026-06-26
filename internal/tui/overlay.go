package tui

// overlay is a modal layer drawn over the scrollback region.
type overlay interface {
	// anchor returns the 1-based terminal row of the overlay's first line and
	// the ANSI-bearing lines to draw (no trailing newline per line).
	render(scrollRows, cols int) (anchorRow int, lines []string)
}