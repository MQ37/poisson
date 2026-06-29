package tui

import "strings"

// approvalOverlay is a centered allow/deny prompt over the scrollback region.
// Long commands are wrapped and scrollable with ↑/↓ while approving.
type approvalOverlay struct {
	command     string
	description string
	workdir     string
	scroll      int
}

func newApprovalOverlay(command, description, workdir string) *approvalOverlay {
	return &approvalOverlay{
		command:     command,
		description: resolveApprovalPurpose(command, description),
		workdir:     workdir,
	}
}

func (o *approvalOverlay) scrollBy(delta int) {
	o.scroll += delta
	o.clampScroll()
}

func (o *approvalOverlay) clampScroll() {
	if o.scroll < 0 {
		o.scroll = 0
	}
}

func (o *approvalOverlay) bodyLines(inner int) (purpose, sep string, cmdLines []string, scrollHint string, maxBody int) {
	purpose = dim + "Purpose: " + reset + truncatePlain(o.description, inner-10)
	sep = dim + strings.Repeat("─", inner) + reset
	cmdLines = wrapPlain("$ "+o.command, inner)
	if len(cmdLines) == 0 {
		cmdLines = []string{"$ "}
	}
	maxBody = 8
	if len(cmdLines) > maxBody {
		scrollHint = dim + "↑↓ scroll command" + reset
		if o.scroll > len(cmdLines)-maxBody {
			o.scroll = len(cmdLines) - maxBody
		}
		o.clampScroll()
		if o.scroll > len(cmdLines)-maxBody {
			o.scroll = len(cmdLines) - maxBody
		}
		cmdLines = cmdLines[o.scroll : o.scroll+maxBody]
	}
	return purpose, sep, cmdLines, scrollHint, maxBody
}

func (o *approvalOverlay) render(scrollRows, cols int) (int, []string) {
	if scrollRows < 5 || cols < 24 {
		return 1, o.fallbackLines(cols)
	}

	width := boxInnerWidth(cols, cols-4)
	inner := width - 6
	if inner < 12 {
		inner = 12
	}

	maxBody := scrollRows - 7
	if maxBody < 3 {
		maxBody = 3
	}

	purpose := dim + "Purpose: " + reset + truncatePlain(o.description, inner-10)
	wd := ""
	if strings.TrimSpace(o.workdir) != "" {
		wd = dim + "cwd: " + reset + truncatePlain(o.workdir, inner-6)
	}
	sep := dim + strings.Repeat("─", inner) + reset

	cmdLines := wrapPlain("$ "+o.command, inner)
	if len(cmdLines) == 0 {
		cmdLines = []string{"$ "}
	}
	scrollHint := ""
	if len(cmdLines) > maxBody {
		scrollHint = dim + "↑↓ scroll command" + reset
		if o.scroll > len(cmdLines)-maxBody {
			o.scroll = len(cmdLines) - maxBody
		}
		o.clampScroll()
		cmdLines = cmdLines[o.scroll : o.scroll+maxBody]
	}

	footer := "[A/y/Enter] Allow   [D/n/Esc] Deny   Ctrl+C cancel"

	var body []string
	body = append(body, purpose)
	if wd != "" {
		body = append(body, wd)
	}
	body = append(body, sep)
	body = append(body, cmdLines...)
	if scrollHint != "" {
		body = append(body, scrollHint)
	}
	body = append(body, footer)

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
	b.WriteString("  " + dim + "Purpose: " + reset + truncatePlain(o.description, cols-14) + "\n")
	if strings.TrimSpace(o.workdir) != "" {
		b.WriteString("  " + dim + "cwd: " + reset + truncatePlain(o.workdir, cols-10) + "\n")
	}
	for _, ln := range wrapPlain("$ "+o.command, cols-4) {
		b.WriteString("  " + truncatePlain(ln, cols-4) + "\n")
	}
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