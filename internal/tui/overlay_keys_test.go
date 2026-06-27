package tui

import "testing"

func TestIsArrowUpKittyCSIu(t *testing.T) {
	raw := []byte{27, '[', '5', '7', '3', '5', '2', 'u'}
	if !isArrowUp(raw) {
		t.Fatal("expected kitty up arrow")
	}
	if isArrowDown(raw) {
		t.Fatal("kitty up should not match down")
	}
}

func TestIsArrowDownKittyCSIu(t *testing.T) {
	raw := []byte{27, '[', '5', '7', '3', '5', '3', 'u'}
	if !isArrowDown(raw) {
		t.Fatal("expected kitty down arrow")
	}
}

func TestPickerOverlayArrowKeys(t *testing.T) {
	p := newPickerOverlay("Providers", []pickerItem{
		{id: "anthropic", label: "anthropic"},
		{id: "ollama", label: "ollama"},
	}, "anthropic", nil)
	if p.idx != 0 {
		t.Fatalf("idx=%d", p.idx)
	}
	handled, _, _ := p.feedKey(arrowDownBytes())
	if !handled || p.idx != 1 {
		t.Fatalf("down: handled=%v idx=%d", handled, p.idx)
	}
	handled, _, _ = p.feedKey(arrowUpBytes())
	if !handled || p.idx != 0 {
		t.Fatalf("up: handled=%v idx=%d", handled, p.idx)
	}
	kittyDown := []byte{27, '[', '5', '7', '3', '5', '3', 'u'}
	handled, _, _ = p.feedKey(kittyDown)
	if !handled || p.idx != 1 {
		t.Fatalf("kitty down: handled=%v idx=%d", handled, p.idx)
	}
}

func TestPaletteOverlayArrowKeys(t *testing.T) {
	p := newPaletteOverlay(nil)
	handled, _, _ := p.feedKey(arrowDownBytes())
	if !handled || p.idx != 1 {
		t.Fatalf("palette down: handled=%v idx=%d", handled, p.idx)
	}
}

func TestHandleKeyOverlayPreservesChainedPicker(t *testing.T) {
	tui := newTestTUIv2()
	pal := newPaletteOverlay(func(cmd string) error {
		if cmd == "/providers" {
			tui.activeOverlay = newPickerOverlay("Providers", []pickerItem{
				{id: "ollama", label: "ollama"},
			}, "", nil)
		}
		return nil
	})
	pal.filter = "providers"
	pal.idx = 0
	tui.activeOverlay = pal
	if !tui.handleKeyOverlay([]byte{'\r'}) {
		t.Fatal("enter not handled")
	}
	if tui.activeOverlay == nil {
		t.Fatal("provider picker was cleared after palette selection")
	}
	if _, ok := tui.activeOverlay.(*pickerOverlay); !ok {
		t.Fatalf("expected pickerOverlay, got %T", tui.activeOverlay)
	}
}