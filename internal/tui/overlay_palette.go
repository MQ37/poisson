package tui

type paletteItem struct {
	cmd  string
	desc string
}

var paletteCommands = []paletteItem{
	{"/help", "show commands"},
	{"/new", "new session"},
	{"/sessions", "session picker"},
	{"/resume", "resume session"},
	{"/model", "model picker"},
	{"/models", "list models"},
	{"/providers", "provider picker"},
	{"/cost", "token cost breakdown"},
	{"/clear", "clear scrollback"},
	{"/search", "search messages"},
	{"/fork", "fork session"},
	{"/undo", "undo last turn"},
	{"/reload", "reload config"},
	{"/effort", "set reasoning effort"},
	{"/btw", "side question (floating box)"},
	{"/quit", "exit"},
}

type paletteOverlay struct {
	filter string
	idx    int
	onRun  func(cmd string) error
}

func newPaletteOverlay(onRun func(string) error) *paletteOverlay {
	return &paletteOverlay{onRun: onRun}
}

func (p *paletteOverlay) filtered() []paletteItem {
	if p.filter == "" {
		return paletteCommands
	}
	var out []paletteItem
	for _, it := range paletteCommands {
		if fuzzyScore(p.filter, it.cmd) >= 0 || fuzzyScore(p.filter, it.desc) >= 0 {
			out = append(out, it)
		}
	}
	return out
}

func (p *paletteOverlay) render(scrollRows, cols int) (int, []string) {
	visible := p.filtered()
	if p.idx >= len(visible) {
		p.idx = len(visible) - 1
	}
	if p.idx < 0 {
		p.idx = 0
	}
	maxRows := scrollRows - 4
	if maxRows < 6 {
		maxRows = 6
	}
	start := 0
	if p.idx >= maxRows-1 {
		start = p.idx - maxRows + 2
	}
	end := start + maxRows
	if end > len(visible) {
		end = len(visible)
		start = end - maxRows
		if start < 0 {
			start = 0
		}
	}

	var lines []string
	title := fgYellow + bold + " command palette" + reset
	if p.filter != "" {
		title += dim + "  " + p.filter + reset
	}
	lines = append(lines, title)
	for i := start; i < end && i < len(visible); i++ {
		it := visible[i]
		marker := "  "
		style := ""
		if i == p.idx {
			marker = "▶ "
			style = fgCyan + bold
		}
		lines = append(lines, style+marker+it.cmd+dim+"  "+it.desc+reset)
	}
	lines = append(lines, dim+"  ↑↓ move · Enter run · Esc cancel · type to filter"+reset)

	height := len(lines)
	anchor := (scrollRows - height) / 2
	if anchor < 1 {
		anchor = 1
	}
	out := make([]string, len(lines))
	for i, ln := range lines {
		out[i] = truncateToWidth(ln, cols)
	}
	return anchor, out
}

func (p *paletteOverlay) feedKey(data []byte) (handled bool, done bool, cancel bool) {
	if isArrowUp(data) {
		if p.idx > 0 {
			p.idx--
		}
		return true, false, false
	}
	if isArrowDown(data) {
		if p.idx < len(p.filtered())-1 {
			p.idx++
		}
		return true, false, false
	}
	if containsSubmitKey(data) {
		vis := p.filtered()
		if len(vis) == 0 {
			return true, false, true
		}
		if p.onRun != nil {
			_ = p.onRun(vis[p.idx].cmd)
		}
		return true, true, false
	}
	for _, b := range data {
		if b == 27 && !hasCSI(data) {
			return true, false, true
		}
	}
	if containsBackspace(data) {
		runes := []rune(p.filter)
		if len(runes) > 0 {
			p.filter = string(runes[:len(runes)-1])
			p.idx = 0
		}
		return true, false, false
	}
	for _, b := range data {
		if b >= 32 && b != 127 {
			p.filter += string(b)
			p.idx = 0
			return true, false, false
		}
	}
	return false, false, false
}