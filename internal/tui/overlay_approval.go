package tui

import "strings"

// approvalOverlay is a centered allow/deny prompt over the scrollback region.
type approvalOverlay struct {
	command     string
	description string
}

func newApprovalOverlay(command, description string) *approvalOverlay {
	return &approvalOverlay{
		command:     command,
		description: resolveApprovalPurpose(command, description),
	}
}

func (o *approvalOverlay) render(scrollRows, cols int) (int, []string) {
	if scrollRows < 5 || cols < 24 {
		return 1, o.fallbackLines(cols)
	}

	width := boxInnerWidth(cols, 68)
	inner := width - 6
	if inner < 12 {
		inner = 12
	}

	var body []string
	body = append(body, "$ "+truncatePlain(o.command, inner-4))
	body = append(body, dim+"Purpose: "+truncatePlain(o.description, inner-10)+reset)
	body = append(body, "[A/y/Enter] Allow   [D/n/Esc] Deny   Ctrl+C cancel")

	var lines []string
	lines = append(lines, boxTopBorder("approval required", width))
	for _, ln := range body {
		lines = append(lines, boxBodyLine(width, ln))
	}
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
	return anchor, lines
}

func (o *approvalOverlay) fallbackLines(cols int) []string {
	var b strings.Builder
	b.WriteString(fgYellow + bold + "⚠ approval required" + reset + "\n")
	b.WriteString("  $ " + truncatePlain(o.command, cols-4) + "\n")
	b.WriteString("  " + dim + "Purpose: " + truncatePlain(o.description, cols-12) + reset + "\n")
	b.WriteString(dim + "  [A/y/Enter] Allow   [D/n/Esc] Deny   Ctrl+C cancel" + reset)
	return strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
}

func truncatePlain(s string, width int) string {
	if width < 1 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	return string(runes[:width-1]) + "…"
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// approvalKeyAllowed maps approval input bytes to allow/deny. ok reports
// whether the chunk contained a recognized approval answer.
func approvalKeyAllowed(data []byte) (allowed, ok bool) {
	var d Decoder
	for _, k := range d.Push(data) {
		if a, hit := keyApprovalAnswer(k); hit {
			return a, true
		}
	}
	return false, false
}

// overlayHeight returns how many rows an overlay needs (for dirty marking).
func overlayHeight(o overlay, scrollRows, cols int) int {
	_, lines := o.render(scrollRows, cols)
	return len(lines)
}

// formatApprovalResult is the scrollback line after the user decides.
func formatApprovalResult(allowed bool) string {
	if allowed {
		return "  allow"
	}
	return "  deny"
}