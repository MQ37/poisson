package tui

import (
	"strings"
	"testing"
)

func TestRenderBoxedListHasBorder(t *testing.T) {
	body := []string{"  alpha", fgCyan + bold + "▶ beta" + reset}
	_, lines := renderBoxedList("Providers", "", body, 24, 80, 72, "")
	if len(lines) < 4 {
		t.Fatalf("expected boxed lines, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "╭") || !strings.Contains(lines[len(lines)-1], "╰") {
		t.Fatalf("missing box borders: %v", lines)
	}
	if !strings.Contains(stripANSI(lines[0]), "Providers") {
		t.Fatalf("title missing: %q", lines[0])
	}
	if !strings.Contains(lines[len(lines)-2], overlayFooterHint) {
		t.Fatalf("footer hint missing")
	}
}

func TestPickerOverlayBoxedRender(t *testing.T) {
	p := newPickerOverlay("Providers", []pickerItem{
		{id: "anthropic", label: "anthropic", hint: "ok"},
		{id: "ollama", label: "ollama"},
	}, "anthropic", nil)
	anchor, lines := p.render(24, 80)
	if anchor < 1 || len(lines) < 5 {
		t.Fatalf("anchor=%d lines=%d", anchor, len(lines))
	}
	if !strings.Contains(lines[0], "╭") {
		t.Fatalf("expected box top: %q", lines[0])
	}
}

func TestPickerClickRowSelects(t *testing.T) {
	var picked string
	p := newPickerOverlay("Providers", []pickerItem{
		{id: "anthropic", label: "anthropic"},
		{id: "ollama", label: "ollama"},
	}, "", func(id string) error {
		picked = id
		return nil
	})
	p.render(24, 80)
	handled, done := p.clickRow(p.chrome.itemLine0 + 1)
	if !handled || !done || picked != "ollama" {
		t.Fatalf("handled=%v done=%v picked=%q", handled, done, picked)
	}
}

func TestBoxLinesEqualWidth(t *testing.T) {
	body := []string{"  alpha", fgCyan + bold + "▶ beta" + reset}
	_, lines := renderBoxedList("Models (ollama)", "", body, 24, 80, 72, "")
	if len(lines) < 3 {
		t.Fatal("expected box lines")
	}
	w0 := visibleWidth(lines[0])
	for i, ln := range lines {
		w := visibleWidth(ln)
		if w != w0 {
			t.Fatalf("line %d width %d != top width %d: %q", i, w, w0, stripANSI(ln))
		}
		if w > 80 {
			t.Fatalf("line %d exceeds cols: %d", i, w)
		}
	}
}