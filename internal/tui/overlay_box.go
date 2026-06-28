package tui

import "strings"

const overlayFooterHint = "↑↓ move · Enter · Esc · click · Ctrl+C"

// listBoxChrome records layout from the last render for mouse hit-testing.
type listBoxChrome struct {
	anchor    int
	itemLine0 int
	itemCount int
	itemStart int // index in filtered list of first visible row
}

// boxInnerWidth picks a centered modal width from terminal cols.
func boxInnerWidth(cols, maxInner int) int {
	inner := cols - 4
	if inner > maxInner {
		inner = maxInner
	}
	if inner < 24 {
		inner = cols - 2
	}
	if inner < 12 {
		inner = 12
	}
	return inner
}

func boxTopBorder(title string, width int) string {
	titlePlain := stripANSI(title)
	prefix := "╭─ " + titlePlain + " "
	fill := width - visibleWidth(prefix) - 1
	if fill < 0 {
		fill = 0
	}
	return fgYellow + bold + prefix + strings.Repeat("─", fill) + "╮" + reset
}

func boxBottomBorder(width int) string {
	fill := width - 2
	if fill < 0 {
		fill = 0
	}
	return fgYellow + bold + "╰" + strings.Repeat("─", fill) + "╯" + reset
}

func boxBodyLine(width int, content string) string {
	inner := width - 6 // │ + two spaces each side
	if inner < 0 {
		inner = 0
	}
	if visibleWidth(content) > inner {
		content = truncateToWidth(content, inner)
	}
	pad := inner - visibleWidth(content)
	if pad < 0 {
		pad = 0
	}
	return fgYellow + bold + "│" + reset + "  " + content + strings.Repeat(" ", pad) + "  " + fgYellow + bold + "│" + reset
}

func boxFooterLine(width int) string {
	return boxBodyLine(width, dim+overlayFooterHint+reset)
}

// renderBoxedList draws a bordered list modal centered in the scroll region.
func renderBoxedList(title, filter string, body []string, scrollRows, cols, maxInner int) (listBoxChrome, []string) {
	chrome := listBoxChrome{}
	if scrollRows < 4 || cols < 20 {
		lines := make([]string, len(body))
		for i, ln := range body {
			lines[i] = truncateToWidth(ln, cols)
		}
		chrome.anchor = 1
		chrome.itemLine0 = 0
		chrome.itemCount = len(body)
		return chrome, lines
	}

	width := boxInnerWidth(cols, maxInner)

	var lines []string
	lines = append(lines, boxTopBorder(title, width))
	if filter != "" {
		lines = append(lines, boxBodyLine(width, dim+"filter: "+filter+reset))
	}
	chrome.itemLine0 = len(lines)
	for _, ln := range body {
		lines = append(lines, boxBodyLine(width, ln))
	}
	chrome.itemCount = len(body)
	lines = append(lines, boxFooterLine(width))
	lines = append(lines, boxBottomBorder(width))

	height := len(lines)
	anchor := (scrollRows-height)/2 + 1
	if anchor < 1 {
		anchor = 1
	}
	if anchor+height-1 > scrollRows {
		anchor = scrollRows - height + 1
		if anchor < 1 {
			anchor = 1
		}
	}
	chrome.anchor = anchor

	out := make([]string, len(lines))
	for i, ln := range lines {
		out[i] = truncateToWidth(ln, cols)
	}
	return chrome, out
}

// listClickOverlay supports mouse row selection on boxed list modals.
type listClickOverlay interface {
	overlay
	listChrome() listBoxChrome
	clickRow(lineInOverlay int) (handled bool, done bool)
}

func asListClickOverlay(o overlay) listClickOverlay {
	if o == nil {
		return nil
	}
	if l, ok := o.(listClickOverlay); ok {
		return l
	}
	return nil
}