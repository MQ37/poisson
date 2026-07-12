package tui

import "strings"

// approvalReply is the user's answer to a pending bash approval: whether it
// was allowed, and (when denied) an optional human-supplied reason forwarded
// to the model so it understands why, not just that.
type approvalReply struct {
	Allowed bool
	Reason  string
}

// approvalOverlay replaces the input region while the user approves a bash command.
// Long commands scroll with ↑/↓ inside the panel.
type approvalOverlay struct {
	command     string
	description string
	workdir     string
	risk        string // "pending", "low", "medium", "high", "failed"
	scroll      int

	// denying is set once the user has committed to denying (d/n/Esc) but
	// hasn't confirmed yet: the panel switches to collecting an optional
	// reason, and reason accumulates what they type until Enter/Esc finalizes
	// the denial (or Ctrl+C sends it immediately with reason left empty).
	denying bool
	reason  string
}

func newApprovalOverlay(command, description, workdir string) *approvalOverlay {
	return &approvalOverlay{
		command:     command,
		description: resolveApprovalPurpose(command, description),
		workdir:     workdir,
		risk:        "pending",
	}
}

// beginDenyReason switches the panel into reason-collection mode.
func (o *approvalOverlay) beginDenyReason() {
	o.denying = true
}

func (o *approvalOverlay) setRisk(risk string) {
	if risk == "" {
		risk = "failed"
	}
	o.risk = risk
}

func approvalRiskLine(risk string) string {
	switch risk {
	case "pending":
		return dim + "Risk: assessing…" + reset
	case "low":
		return fgGreen + bold + "Risk: LOW" + reset
	case "medium":
		return fgYellow + bold + "Risk: MEDIUM" + reset
	case "high":
		return fgRed + bold + "Risk: HIGH" + reset
	case "failed":
		return fgRed + bold + "Risk: classification FAILED — review manually" + reset
	default:
		return dim + "Risk: unknown" + reset
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

func approvalPanelBG() string {
	return bgBlack
}

func approvalPurposeLines(description string, inner int) []string {
	wrapW := inner - 10
	if wrapW < 8 {
		wrapW = 8
	}
	wrapped := wrapPlain(description, wrapW)
	if len(wrapped) == 0 {
		return []string{dim + "Purpose: " + reset + description}
	}
	lines := make([]string, len(wrapped))
	lines[0] = dim + "Purpose: " + reset + wrapped[0]
	indent := strings.Repeat(" ", 10)
	for i := 1; i < len(wrapped); i++ {
		lines[i] = indent + wrapped[i]
	}
	return lines
}

// renderDenyReasonPanel paints the optional-reason prompt shown after the
// user commits to denying (d/n/Esc), replacing the normal approval panel.
func (o *approvalOverlay) renderDenyReasonPanel(panelRows, cols int) []string {
	if panelRows < 3 {
		panelRows = 3
	}
	if cols < 12 {
		cols = 12
	}
	bg := approvalPanelBG()
	mk := func(content string) string { return fillWidthBG(bg, content, cols) }
	blank := mk("")

	title := mk(fgYellow + bold + "Command denied — reason (optional)" + reset)
	footer := mk(dim + "[Enter] send — reason continues the turn, blank stops it · [Ctrl+C] stop now" + reset)

	oneLine := strings.ReplaceAll(strings.TrimSpace(o.command), "\n", " ")
	cmdSummary := mk(dim + "Command: " + reset + truncatePlain(oneLine, cols-12))

	label := "Reason: "
	avail := cols - len([]rune(label)) - 3 // "  " indent + trailing cursor glyph
	if avail < 4 {
		avail = 4
	}
	shown := o.reason
	if rw := []rune(shown); len(rw) > avail {
		// Keep the tail visible while typing long text.
		shown = string(rw[len(rw)-avail:])
	}
	inputLine := mk("  " + label + shown + reset + fgYellow + "█" + reset)

	out := make([]string, panelRows)
	out[0] = title
	out[panelRows-1] = footer
	idx := 1
	out[idx] = cmdSummary
	idx++
	if idx < panelRows-1 {
		out[idx] = blank
		idx++
	}
	if idx < panelRows-1 {
		out[idx] = inputLine
		idx++
	}
	for i := idx; i < panelRows-1; i++ {
		out[i] = blank
	}
	return out
}

// renderInputPanel paints the approval UI into the bottom input region. Each
// line is a full-width opaque background band (panelRows lines total).
func (o *approvalOverlay) renderInputPanel(panelRows, cols int) []string {
	if panelRows < 3 {
		panelRows = 3
	}
	if cols < 12 {
		cols = 12
	}
	if o.denying {
		return o.renderDenyReasonPanel(panelRows, cols)
	}
	bg := approvalPanelBG()
	mk := func(content string) string { return fillWidthBG(bg, content, cols) }
	blank := mk("")

	title := mk(fgYellow + bold + "⚠  Approval required" + reset)
	footer := mk(dim + "[A/y/Enter] Allow · [D/n/Esc] Deny · Tab/PgUp review convo · Ctrl+C cancel" + reset)

	var meta []string
	for _, ln := range approvalPurposeLines(o.description, cols) {
		meta = append(meta, mk(ln))
	}
	meta = append(meta, mk(approvalRiskLine(o.risk)))
	if wd := strings.TrimSpace(o.workdir); wd != "" {
		meta = append(meta, mk(dim+"cwd: "+reset+truncatePlain(wd, cols-8)))
	}

	cmdAll := approvalCommandLines(o.command, cols-4)
	for i := range cmdAll {
		cmdAll[i] = mk("  " + cmdAll[i])
	}

	cmdRows := panelRows - 2 - len(meta)
	if cmdRows < 1 {
		cmdRows = 1
	}
	var scrollHint string
	if len(cmdAll) > cmdRows {
		cmdRows--
		if cmdRows < 1 {
			cmdRows = 1
		}
		scrollHint = mk(dim + "  ↑↓ scroll command" + reset)
		if o.scroll > len(cmdAll)-cmdRows {
			o.scroll = len(cmdAll) - cmdRows
		}
		o.clampScroll()
		cmdAll = cmdAll[o.scroll : o.scroll+cmdRows]
	}

	out := make([]string, panelRows)
	out[0] = title
	out[panelRows-1] = footer
	idx := 1
	for _, m := range meta {
		if idx >= panelRows-1 {
			break
		}
		out[idx] = m
		idx++
	}
	for _, c := range cmdAll {
		if idx >= panelRows-1 {
			break
		}
		out[idx] = c
		idx++
	}
	if scrollHint != "" && idx < panelRows-1 {
		out[idx] = scrollHint
		idx++
	}
	for i := idx; i < panelRows-1; i++ {
		out[i] = blank
	}
	return out
}

// render implements overlay for tests and fallback; approval is painted in the
// input region, not over scrollback.
func (o *approvalOverlay) render(scrollRows, cols int) (int, []string) {
	_ = scrollRows
	lines := o.renderInputPanel(8, cols)
	if len(lines) == 0 {
		return 1, o.fallbackLines(cols)
	}
	return 1, lines
}

func (o *approvalOverlay) fallbackLines(cols int) []string {
	return o.renderInputPanel(6, cols)
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
