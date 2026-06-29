package tui

import (
	"strings"
	"testing"
)

func TestPickerOverlayRender(t *testing.T) {
	p := newPickerOverlay("Test", []pickerItem{
		{id: "a", label: "alpha", hint: "one"},
		{id: "b", label: "beta", hint: "two"},
	}, "a", nil)
	anchor, lines := p.render(20, 80)
	if anchor < 1 || len(lines) < 3 {
		t.Fatalf("anchor=%d lines=%d", anchor, len(lines))
	}
}

func TestPickerEffortItems(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	a.SetEffort("high")
	items := pickerEffortItems(cmdHost(newTUIWithAgent(a, sessionID)))
	if len(items) < 1 {
		t.Fatal("expected at least one effort level")
	}
	found := false
	for _, it := range items {
		if it.id == "high" && strings.Contains(it.hint, "current") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected current marker on high: %v", items)
	}
}

func TestIntersectEffortLevels(t *testing.T) {
	got := intersectEffortLevels([]string{"high", "max"}, effortPickerLevels)
	if len(got) != 1 || got[0] != "high" {
		t.Fatalf("got %v, want [high]", got)
	}
}

func TestPaletteOverlayFilter(t *testing.T) {
	p := newPaletteOverlay(nil)
	p.filter = "cost"
	vis := p.filtered()
	if len(vis) != 1 || vis[0].id != "/cost" {
		t.Fatalf("filtered = %v", vis)
	}
}