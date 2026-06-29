package tui

import (
	"errors"
	"fmt"
	"time"

	"poisson/internal/auth"
	"poisson/internal/provider"
	"poisson/internal/store"
)

// effortPickerLevels are the effort options shown in the picker UI.
var effortPickerLevels = []string{"low", "medium", "high", "xhigh"}

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
		if id == "" {
			return false
		}
		if onPick != nil {
			if err := onPick(id); err != nil {
				return false
			}
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

func pickerEffortItems(h commandHost) []pickerItem {
	a := h.Agent()
	levels := effortPickerLevels
	if s, ok := provider.GetModelSettings(a.Provider().ID(), a.Model()); ok && s.SupportsEffort && len(s.EffortLevels) > 0 {
		levels = intersectEffortLevels(s.EffortLevels, effortPickerLevels)
		if len(levels) == 0 {
			levels = effortPickerLevels
		}
	}
	cur := a.Effort()
	items := make([]pickerItem, len(levels))
	for i, lv := range levels {
		hint := "thinking depth"
		if cur == lv {
			hint = "current · " + hint
		}
		items[i] = pickerItem{id: lv, label: lv, hint: hint}
	}
	return items
}

func intersectEffortLevels(supported, allowed []string) []string {
	set := make(map[string]struct{}, len(supported))
	for _, s := range supported {
		set[s] = struct{}{}
	}
	var out []string
	for _, a := range allowed {
		if _, ok := set[a]; ok {
			out = append(out, a)
		}
	}
	return out
}

func pickerSessionItems(h commandHost) ([]pickerItem, error) {
	a := h.Agent()
	curID := h.SessionID()
	sessions, err := a.Store().ListSessions(20, 0)
	if err != nil {
		return nil, err
	}
	items := make([]pickerItem, 0, len(sessions)+1)
	curFound := false
	for _, sess := range sessions {
		if sess.ID == curID {
			curFound = true
		}
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
	if curID != "" && !curFound {
		if _, err := a.Store().GetSession(curID); errors.Is(err, store.ErrNotFound) {
			label := curID
			if len(label) > 12 {
				label = label[:12] + "…"
			}
			items = append([]pickerItem{{
				id:    curID,
				label: label,
				hint:  fmt.Sprintf("current · unsaved · %s/%s", a.Provider().ID(), a.Model()),
			}}, items...)
		}
	}
	return items, nil
}