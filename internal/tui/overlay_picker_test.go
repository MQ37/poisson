package tui

import (
	"strings"
	"testing"

	"poisson/internal/provider"
	"poisson/internal/testutil"
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

func TestPickerEffortIncludesMax(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	a.SetProvider(provider.NewOllamaProvider("http://localhost:11434", "glm-5.2:cloud"))
	a.SetModel("glm-5.2:cloud")
	items := pickerEffortItems(cmdHost(newTUIWithAgent(a, sessionID)))
	hasMax := false
	for _, it := range items {
		if it.id == "max" {
			hasMax = true
		}
	}
	if !hasMax {
		t.Fatalf("expected max in effort picker: %v", items)
	}
}

func TestPickerProviderItemsOllamaNoAuth(t *testing.T) {
	testutil.TempHome(t) // isolate auth store to an empty temp HOME
	_, a, sessionID := newTestStoreAndAgent(t)
	items := pickerProviderItems(cmdHost(newTUIWithAgent(a, sessionID)))

	var ollama, anthropic *pickerItem
	for i := range items {
		switch items[i].id {
		case "ollama":
			ollama = &items[i]
		case "anthropic":
			anthropic = &items[i]
		}
	}
	if ollama == nil {
		t.Fatal("ollama not in provider picker")
	}
	// Ollama needs no credentials — it must never show as "not configured".
	if strings.Contains(ollama.hint, "not configured") {
		t.Errorf("ollama should not be 'not configured': %q", ollama.hint)
	}
	if !strings.Contains(ollama.hint, "no auth needed") {
		t.Errorf("ollama hint = %q, want 'no auth needed'", ollama.hint)
	}
	// A credential-based provider with no auth should still show not configured.
	if anthropic == nil || !strings.Contains(anthropic.hint, "not configured") {
		t.Errorf("anthropic (no creds) should be 'not configured', got %v", anthropic)
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