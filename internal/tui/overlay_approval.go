package tui

import "strings"

// approvalOverlay is a centered allow/deny prompt over the scrollback region.
type approvalOverlay struct {
	command     string
	description string
}

func newApprovalOverlay(command, description string) *approvalOverlay {
	return &approvalOverlay{command: command, description: description}
}

func (o *approvalOverlay) render(scrollRows, cols int) (int, []string) {
	if scrollRows < 5 || cols < 24 {
		return 1, o.fallbackLines(cols)
	}
	inner := cols - 4
	if inner > 68 {
		inner = 68
	}
	if inner < 20 {
		inner = cols - 2
	}

	var body []string
	body = append(body, "$ "+truncatePlain(o.command, inner-4))
	purpose := o.description
	if purpose == "" {
		purpose = "(no description provided)"
	}
	body = append(body, dim+"Purpose: "+truncatePlain(purpose, inner-10)+reset)
	body = append(body, "[A] Allow   [D] Deny")

	height := len(body) + 2 // top + bottom border
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

	top := "╭" + strings.Repeat("─", inner) + "╮"
	bot := "╰" + strings.Repeat("─", inner) + "╯"
	lines := []string{
		fgYellow + bold + top + reset,
	}
	lines = append(lines, fgYellow+bold+"│"+reset+" "+fgYellow+bold+"⚠ approval required"+reset+
		strings.Repeat(" ", max0(inner-21))+" "+fgYellow+bold+"│"+reset)
	for _, ln := range body {
		pad := inner - visibleWidth(ln)
		if pad < 0 {
			pad = 0
		}
		lines = append(lines, fgYellow+bold+"│"+reset+" "+ln+strings.Repeat(" ", pad)+" "+fgYellow+bold+"│"+reset)
	}
	lines = append(lines, fgYellow+bold+bot+reset)
	return anchor, lines
}

func (o *approvalOverlay) fallbackLines(cols int) []string {
	var b strings.Builder
	b.WriteString(fgYellow + bold + "⚠ approval required" + reset + "\n")
	b.WriteString("  $ " + truncatePlain(o.command, cols-4) + "\n")
	purpose := o.description
	if purpose == "" {
		purpose = "(no description provided)"
	}
	b.WriteString("  " + dim + "Purpose: " + truncatePlain(purpose, cols-12) + reset + "\n")
	b.WriteString(dim + "  [A] Allow   [D] Deny" + reset)
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
	for _, b := range data {
		switch b {
		case 'a', 'A', 'y', 'Y', '\r':
			return true, true
		case 'd', 'D', 'n', 'N', 3, 27: // Ctrl+C, Esc
			return false, true
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

