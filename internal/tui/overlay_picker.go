package tui

import (
	"fmt"
	"time"

	"poisson/internal/auth"
)

// pickerItem is one row in a picker overlay.
type pickerItem struct {
	id    string
	label string
	hint  string
}

type pickerOverlay struct {
	title   string
	items   []pickerItem
	current string
	filter  string
	idx     int
	onPick  func(id string) error
	chrome  listBoxChrome
}

func newPickerOverlay(title string, items []pickerItem, current string, onPick func(string) error) *pickerOverlay {
	idx := 0
	for i, it := range items {
		if it.id == current {
			idx = i
			break
		}
	}
	return &pickerOverlay{title: title, items: items, current: current, idx: idx, onPick: onPick}
}

func (p *pickerOverlay) filtered() []pickerItem {
	if p.filter == "" {
		return p.items
	}
	var out []pickerItem
	for _, it := range p.items {
		if fuzzyScore(p.filter, it.label) >= 0 || fuzzyScore(p.filter, it.hint) >= 0 {
			out = append(out, it)
		}
	}
	return out
}

func (p *pickerOverlay) render(scrollRows, cols int) (int, []string) {
	visible := p.filtered()
	if len(visible) == 0 {
		body := []string{dim + "(no matches)" + reset}
		chrome, lines := renderBoxedList(p.title, p.filter, body, scrollRows, cols, 76)
		p.chrome = chrome
		return p.chrome.anchor, lines
	}

	if p.idx >= len(visible) {
		p.idx = len(visible) - 1
	}
	if p.idx < 0 {
		p.idx = 0
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
	for i := start; i < end; i++ {
		it := visible[i]
		marker := "  "
		style := ""
		if i == p.idx {
			marker = fgCyan + bold + "▶ " + reset
			style = fgCyan + bold
		}
		cur := ""
		if it.id == p.current {
			cur = dim + " (current)" + reset
		}
		hint := ""
		if it.hint != "" {
			hint = dim + "  " + truncatePlain(it.hint, 48) + reset
		}
		body = append(body, style+marker+it.label+reset+cur+hint)
	}

	chrome, lines := renderBoxedList(p.title, p.filter, body, scrollRows, cols, 76)
	p.chrome = chrome
	return p.chrome.anchor, lines
}

func (p *pickerOverlay) listChrome() listBoxChrome { return p.chrome }

func (p *pickerOverlay) clickRow(lineInOverlay int) (handled bool, done bool) {
	if lineInOverlay < p.chrome.itemLine0 || lineInOverlay >= p.chrome.itemLine0+p.chrome.itemCount {
		return false, false
	}
	off := lineInOverlay - p.chrome.itemLine0
	p.idx = p.chrome.itemStart + off
	vis := p.filtered()
	if p.idx < 0 || p.idx >= len(vis) {
		return true, false
	}
	if p.onPick != nil {
		_ = p.onPick(vis[p.idx].id)
	}
	return true, true
}

func (p *pickerOverlay) feedKey(data []byte) (handled bool, done bool, cancel bool) {
	if isArrowUp(data) {
		if p.idx > 0 {
			p.idx--
		}
		return true, false, false
	}
	if isArrowDown(data) {
		vis := p.filtered()
		if p.idx < len(vis)-1 {
			p.idx++
		}
		return true, false, false
	}
	if containsSubmitKey(data) {
		vis := p.filtered()
		if len(vis) == 0 {
			return true, false, true
		}
		if p.onPick != nil {
			_ = p.onPick(vis[p.idx].id)
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

func containsBackspace(data []byte) bool {
	for _, b := range data {
		if b == 8 || b == 127 {
			return true
		}
	}
	return false
}

// keyOverlay is an overlay that consumes keyboard input until done/cancel.
type keyOverlay interface {
	overlay
	feedKey(data []byte) (handled bool, done bool, cancel bool)
}

func asKeyOverlay(o overlay) keyOverlay {
	if o == nil {
		return nil
	}
	if k, ok := o.(keyOverlay); ok {
		return k
	}
	return nil
}

func pickerProviderItems(h commandHost) []pickerItem {
	store, _ := auth.Load()
	providers := []struct {
		id, desc string
	}{
		{"anthropic", "Claude API/OAuth"},
		{"ollama", "local models"},
		{"xai", "Grok OAuth"},
	}
	var items []pickerItem
	for _, p := range providers {
		status := "✗ not configured"
		if e, ok := store[p.id]; ok && (e.Type == "none" || e.Key != "" || e.Access != "") {
			status = "✓ configured"
		}
		items = append(items, pickerItem{
			id:    p.id,
			label: p.id,
			hint:  status + " · " + p.desc,
		})
	}
	_ = h.Agent().Provider().ID()
	return items
}

func pickerModelItems(h commandHost) ([]pickerItem, error) {
	a := h.Agent()
	models, err := a.Provider().Models()
	if err != nil {
		return nil, err
	}
	items := make([]pickerItem, len(models))
	for i, m := range models {
		items[i] = pickerItem{
			id:    m.ID,
			label: m.ID,
			hint:  fmt.Sprintf("ctx=%d", m.ContextWindow),
		}
	}
	return items, nil
}

func pickerSessionItems(h commandHost) ([]pickerItem, error) {
	a := h.Agent()
	sessions, err := a.Store().ListSessions(20, 0)
	if err != nil {
		return nil, err
	}
	items := make([]pickerItem, 0, len(sessions))
	for _, sess := range sessions {
		msgCount := 0
		if msgs, err := a.Store().GetMessages(sess.ID); err == nil {
			msgCount = len(msgs)
		}
		date := time.Unix(sess.CreatedAt, 0).Format("2006-01-02")
		label := sess.ID
		if len(label) > 12 {
			label = label[:12] + "…"
		}
		items = append(items, pickerItem{
			id:    sess.ID,
			label: label,
			hint:  fmt.Sprintf("%s  %d msgs  %s/%s", date, msgCount, sess.Provider, sess.Model),
		})
	}
	return items, nil
}