package tui

// filterableListItem is one row in a fuzzy-filtered list overlay.
type filterableListItem struct {
	id    string
	label string
	hint  string
	// named marks a row with an explicit, human-given title (session picker
	// only) — gates the Ctrl+N named-only filter.
	named bool
}

// filterableListOverlay is a boxed, filterable list with keyboard and mouse
// selection. Used by provider/model/session pickers and the command palette.
type filterableListOverlay struct {
	title     string
	items     []filterableListItem
	currentID string
	filter    string
	idx       int
	pick      func(id string) bool // returns done
	chrome    listBoxChrome
	listWidth int

	// onDelete, when set (session picker only), enables Ctrl+D to delete the
	// selected row after an Enter confirmation. pendingDeleteID holds the row
	// awaiting confirmation; note is a transient one-line status (cleared on the
	// next key).
	onDelete        func(id string) error
	pendingDeleteID string
	note            string

	// namedFilterEnabled, when set (session picker only), enables Ctrl+N to
	// toggle namedOnly: hide rows without an explicit title.
	namedFilterEnabled bool
	namedOnly          bool

	// footerHint overrides the default keybinding footer (empty = default).
	footerHint string
}

// titleForRender shows the delete confirmation / transient note in the box
// title so item-line/click mapping is unaffected.
func (p *filterableListOverlay) titleForRender() string {
	if p.pendingDeleteID != "" {
		return "Delete " + truncatePlain(p.labelFor(p.pendingDeleteID), 24) + "?  Enter = delete · Esc = cancel"
	}
	if p.note != "" {
		return p.note
	}
	return p.title
}

func (p *filterableListOverlay) labelFor(id string) string {
	for _, it := range p.items {
		if it.id == id {
			return it.label
		}
	}
	return id
}

func (p *filterableListOverlay) removeItem(id string) {
	out := p.items[:0]
	for _, it := range p.items {
		if it.id != id {
			out = append(out, it)
		}
	}
	p.items = out
	if vis := p.filtered(); p.idx >= len(vis) {
		p.idx = len(vis) - 1
	}
	if p.idx < 0 {
		p.idx = 0
	}
}

func newFilterableListOverlay(title string, items []filterableListItem, currentID string, pick func(string) bool, listWidth int) *filterableListOverlay {
	idx := 0
	for i, it := range items {
		if it.id == currentID {
			idx = i
			break
		}
	}
	if listWidth <= 0 {
		listWidth = boxListMaxInner
	}
	return &filterableListOverlay{
		title:     title,
		items:     items,
		currentID: currentID,
		idx:       idx,
		pick:      pick,
		listWidth: listWidth,
	}
}

func (p *filterableListOverlay) filtered() []filterableListItem {
	items := p.items
	if p.namedOnly {
		var named []filterableListItem
		for _, it := range items {
			if it.named {
				named = append(named, it)
			}
		}
		items = named
	}
	if p.filter == "" {
		return items
	}
	var out []filterableListItem
	for _, it := range items {
		if fuzzyScore(p.filter, it.label) >= 0 || fuzzyScore(p.filter, it.hint) >= 0 {
			out = append(out, it)
		}
	}
	return out
}

func (p *filterableListOverlay) render(scrollRows, cols int) (int, []string) {
	visible := p.filtered()
	if len(visible) == 0 {
		body := []string{dim + "(no matches)" + reset}
		chrome, lines := renderBoxedList(p.titleForRender(), p.filter, body, scrollRows, cols, p.listWidth, p.footerHint)
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
		if it.id == p.currentID {
			cur = dim + " (current)" + reset
		}
		hint := ""
		if it.hint != "" {
			hint = dim + "  " + truncatePlain(it.hint, 48) + reset
		}
		body = append(body, style+marker+it.label+reset+cur+hint)
	}

	chrome, lines := renderBoxedList(p.titleForRender(), p.filter, body, scrollRows, cols, p.listWidth, p.footerHint)
	p.chrome = chrome
	return p.chrome.anchor, lines
}

func (p *filterableListOverlay) listChrome() listBoxChrome { return p.chrome }

func (p *filterableListOverlay) clickRow(lineInOverlay int) (handled bool, done bool) {
	if lineInOverlay < p.chrome.itemLine0 || lineInOverlay >= p.chrome.itemLine0+p.chrome.itemCount {
		return false, false
	}
	off := lineInOverlay - p.chrome.itemLine0
	p.idx = p.chrome.itemStart + off
	vis := p.filtered()
	if p.idx < 0 || p.idx >= len(vis) {
		return true, false
	}
	if p.pick != nil {
		done = p.pick(vis[p.idx].id)
	} else {
		done = true
	}
	return true, done
}

func (p *filterableListOverlay) feedKey(k Key) (handled bool, done bool, cancel bool) {
	// The note is transient — any key clears last frame's status.
	p.note = ""

	// Delete-confirmation mode: Enter deletes, any other key cancels. Consume
	// the key either way so Esc cancels the prompt (not the whole overlay).
	if p.pendingDeleteID != "" {
		id := p.pendingDeleteID
		p.pendingDeleteID = ""
		if k.isEnter() {
			if p.onDelete != nil {
				if err := p.onDelete(id); err != nil {
					p.note = "delete failed: " + err.Error()
					return true, false, false
				}
			}
			p.removeItem(id)
			p.note = "deleted"
		}
		return true, false, false
	}

	// Ctrl+N toggles named-only filtering (session picker only; namedFilterEnabled gates it).
	if p.namedFilterEnabled && k.Kind == KeyCtrl && k.Byte == 14 {
		p.namedOnly = !p.namedOnly
		if p.namedOnly {
			p.note = "named only"
		} else {
			p.note = "all sessions"
		}
		if vis := p.filtered(); p.idx >= len(vis) {
			p.idx = len(vis) - 1
		}
		if p.idx < 0 {
			p.idx = 0
		}
		return true, false, false
	}

	// Ctrl+D deletes the selected row (session picker only; onDelete gates it).
	if p.onDelete != nil && k.Kind == KeyCtrl && k.Byte == 4 {
		vis := p.filtered()
		if len(vis) == 0 || vis[p.idx].id == "" {
			return true, false, false
		}
		if vis[p.idx].id == p.currentID {
			p.note = "can't delete the active session"
			return true, false, false
		}
		p.pendingDeleteID = vis[p.idx].id
		return true, false, false
	}

	switch {
	case k.isNavUp():
		if p.idx > 0 {
			p.idx--
		}
		return true, false, false
	case k.isNavDown():
		vis := p.filtered()
		if p.idx < len(vis)-1 {
			p.idx++
		}
		return true, false, false
	case k.isEnter():
		vis := p.filtered()
		if len(vis) == 0 {
			return true, false, true
		}
		if p.pick != nil {
			done = p.pick(vis[p.idx].id)
		} else {
			done = true
		}
		return true, done, false
	case k.Kind == KeyEscape:
		return true, false, true
	case k.Kind == KeyBackspace:
		if trimOverlayFilter(&p.filter) {
			p.syncIdxToCurrent()
		}
		return true, false, false
	case k.Kind == KeyRune:
		if appendOverlayFilterRune(&p.filter, k.Rune) {
			p.idx = 0
		}
		return true, false, false
	case k.Kind == KeyPaste:
		appendOverlayFilterText(&p.filter, k.Text, &p.idx)
		return true, false, false
	}
	return false, false, false
}

// keyOverlay is an overlay that consumes keyboard input until done/cancel.
type keyOverlay interface {
	overlay
	feedKey(k Key) (handled bool, done bool, cancel bool)
}

func (p *filterableListOverlay) syncIdxToCurrent() {
	if p.filter != "" {
		p.idx = 0
		return
	}
	p.idx = 0
	for i, it := range p.items {
		if it.id == p.currentID {
			p.idx = i
			return
		}
	}
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
