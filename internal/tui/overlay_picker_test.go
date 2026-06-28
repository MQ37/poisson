package tui

import "testing"

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

func TestPaletteOverlayFilter(t *testing.T) {
	p := newPaletteOverlay(nil)
	p.filter = "cost"
	vis := p.filtered()
	if len(vis) != 1 || vis[0].id != "/cost" {
		t.Fatalf("filtered = %v", vis)
	}
}