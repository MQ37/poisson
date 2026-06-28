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

type pickerOverlay = filterableListOverlay

func newPickerOverlay(title string, items []pickerItem, current string, onPick func(string) error) *pickerOverlay {
	list := make([]filterableListItem, len(items))
	for i, it := range items {
		list[i] = filterableListItem{id: it.id, label: it.label, hint: it.hint}
	}
	pick := func(id string) bool {
		if onPick != nil {
			_ = onPick(id)
		}
		return true
	}
	return newFilterableListOverlay(title, list, current, pick, boxListMaxInner)
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