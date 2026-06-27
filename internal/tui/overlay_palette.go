package tui

import "errors"

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
	{"/search", "find in scrollback (Ctrl+F)"},
	{"/compact", "compact context (auto)"},
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
	chrome listBoxChrome
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

	if len(visible) == 0 {
		body := []string{dim + "(no matches)" + reset}
		chrome, lines := renderBoxedList("command palette", p.filter, body, scrollRows, cols, boxListMaxInner)
		p.chrome = chrome
		return p.chrome.anchor, lines
	}

	maxRows := scrollRows - 8
	if maxRows < 4 {
		maxRows = 4
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
	p.chrome.itemStart = start

	var body []string
	for i := start; i < end && i < len(visible); i++ {
		it := visible[i]
		marker := "  "
		style := ""
		if i == p.idx {
			marker = fgCyan + bold + "▶ " + reset
			style = fgCyan + bold
		}
		line := style + marker + it.cmd + reset + dim + "  " + it.desc + reset
		body = append(body, line)
	}

	chrome, lines := renderBoxedList("command palette", p.filter, body, scrollRows, cols, 72)
	p.chrome = chrome
	return p.chrome.anchor, lines
}

func (p *paletteOverlay) listChrome() listBoxChrome { return p.chrome }

func (p *paletteOverlay) clickRow(lineInOverlay int) (handled bool, done bool) {
	if lineInOverlay < p.chrome.itemLine0 || lineInOverlay >= p.chrome.itemLine0+p.chrome.itemCount {
		return false, false
	}
	off := lineInOverlay - p.chrome.itemLine0
	p.idx = p.chrome.itemStart + off
	vis := p.filtered()
	if p.idx < 0 || p.idx >= len(vis) {
		return true, false
	}
	if p.onRun != nil {
		if err := p.onRun(vis[p.idx].cmd); errors.Is(err, errQuitSentinel) {
			return true, true
		}
	}
	return true, true
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
			if err := p.onRun(vis[p.idx].cmd); errors.Is(err, errQuitSentinel) {
				return true, true, false
			}
		}
		return true, true, false
	}
	for _, b := range data {
		if b == 27 && !hasCSI(data) {
			return true, false, true
		}
	}
	if containsBackspace(data) {
		if trimOverlayFilter(&p.filter) {
			p.idx = 0
		}
		return true, false, false
	}
	if appendOverlayFilter(&p.filter, data) {
		p.idx = 0
		return true, false, false
	}
	return false, false, false
}