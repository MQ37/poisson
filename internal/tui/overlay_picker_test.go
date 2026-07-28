package tui

import (
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/config"
	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/store"
	"github.com/mq37/poisson/internal/testutil"
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

func TestPickerModelItemsUsesCuratedList(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	a.SetProvider(provider.NewOllamaProvider("http://localhost:11434", "glm-5.2:cloud"))
	a.SetModel("glm-5.2:cloud")
	items, err := pickerModelItems(cmdHost(newTUIWithAgent(a, sessionID)))
	if err != nil {
		t.Fatalf("pickerModelItems: %v", err)
	}
	got := map[string]bool{}
	for _, it := range items {
		got[it.id] = true
	}
	for _, want := range []string{"glm-5.2:cloud", "minimax-m3:cloud", "kimi-k2.7-code:cloud"} {
		if !got[want] {
			t.Errorf("model picker missing curated %q; got %v", want, got)
		}
	}
	// Curated menu must not leak arbitrary live /api/tags entries.
	if got["deepseek-v4-pro:cloud"] {
		t.Error("uncurated live model leaked into the picker")
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

// TestPickerProviderItemsMatchesRegistry checks the /providers picker shows
// exactly the providers in config.Providers, in registry order — this is
// the regression test for the "llamacpp missing from /providers" bug where
// the picker kept its own hardcoded provider list that fell out of sync.
func TestPickerProviderItemsMatchesRegistry(t *testing.T) {
	testutil.TempHome(t)
	_, a, sessionID := newTestStoreAndAgent(t)
	items := pickerProviderItems(cmdHost(newTUIWithAgent(a, sessionID)))
	if len(items) != len(config.Providers) {
		t.Fatalf("got %d picker items, want %d (config.Providers)", len(items), len(config.Providers))
	}
	for i, p := range config.Providers {
		if items[i].id != p.ID {
			t.Errorf("items[%d].id = %q, want %q", i, items[i].id, p.ID)
		}
		if !strings.Contains(items[i].hint, p.Desc) {
			t.Errorf("items[%d].hint = %q, want to contain desc %q", i, items[i].hint, p.Desc)
		}
	}
}

// TestPickerProviderItemsLlamaCppNoAuth checks a second NeedsAuth=false
// provider (not just ollama) is never shown as "not configured".
func TestPickerProviderItemsLlamaCppNoAuth(t *testing.T) {
	testutil.TempHome(t)
	_, a, sessionID := newTestStoreAndAgent(t)
	items := pickerProviderItems(cmdHost(newTUIWithAgent(a, sessionID)))
	for _, it := range items {
		if it.id != "llamacpp" {
			continue
		}
		if strings.Contains(it.hint, "not configured") {
			t.Errorf("llamacpp should not be 'not configured': %q", it.hint)
		}
		if !strings.Contains(it.hint, "no auth needed") {
			t.Errorf("llamacpp hint = %q, want 'no auth needed'", it.hint)
		}
		return
	}
	t.Fatal("llamacpp not in provider picker")
}

func TestPaletteOverlayFilter(t *testing.T) {
	p := newPaletteOverlay(nil)
	p.filter = "cost"
	vis := p.filtered()
	if len(vis) != 1 || vis[0].id != "/cost" {
		t.Fatalf("filtered = %v", vis)
	}
}
func TestSessionPickerItemsMarksNamed(t *testing.T) {
	st, a, sessionID := newTestStoreAndAgent(t)
	named := store.NewSessionID()
	title := "my project"
	st.CreateSession(&store.Session{
		ID: named, Cwd: "/tmp", Provider: "ollama", Model: "test",
		Title: &title, CreatedAt: 1, UpdatedAt: 1,
	})
	unnamed := store.NewSessionID()
	st.CreateSession(&store.Session{
		ID: unnamed, Cwd: "/tmp", Provider: "ollama", Model: "test",
		CreatedAt: 1, UpdatedAt: 1,
	})
	items, err := pickerSessionItems(cmdHost(newTUIWithAgent(a, sessionID)))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, it := range items {
		got[it.id] = it.named
	}
	if !got[named] {
		t.Errorf("session with title should be named: %v", items)
	}
	if got[unnamed] {
		t.Errorf("session without title should not be named: %v", items)
	}
}

func TestFilterableListNamedOnlyToggle(t *testing.T) {
	ov := newFilterableListOverlay("Sessions", []filterableListItem{
		{id: "s1", label: "one", named: true},
		{id: "s2", label: "two"},
	}, "", func(string) bool { return true })
	ov.namedFilterEnabled = true

	if len(ov.filtered()) != 2 {
		t.Fatalf("expected both rows before toggling, got %v", ov.filtered())
	}

	ov.feedKey(Key{Kind: KeyCtrl, Byte: 14})
	if !ov.namedOnly {
		t.Fatal("Ctrl+N should enable namedOnly")
	}
	vis := ov.filtered()
	if len(vis) != 1 || vis[0].id != "s1" {
		t.Fatalf("filtered = %v, want only s1", vis)
	}

	ov.feedKey(Key{Kind: KeyCtrl, Byte: 14})
	if ov.namedOnly {
		t.Fatal("second Ctrl+N should disable namedOnly")
	}
	if len(ov.filtered()) != 2 {
		t.Fatalf("expected both rows after re-toggling, got %v", ov.filtered())
	}
}

func TestFilterableListNamedOnlyIgnoredWhenDisabled(t *testing.T) {
	ov := newFilterableListOverlay("Test", []filterableListItem{
		{id: "a", label: "alpha"},
	}, "", nil)
	handled, _, _ := ov.feedKey(Key{Kind: KeyCtrl, Byte: 14})
	if handled {
		t.Fatal("Ctrl+N must not be consumed when namedFilterEnabled is false")
	}
}

func TestSessionPickerDeleteConfirmFlow(t *testing.T) {
	var deleted string
	newOverlay := func() *filterableListOverlay {
		ov := newFilterableListOverlay("Sessions", []filterableListItem{
			{id: "s1", label: "one"},
			{id: "s2", label: "two"},
			{id: "cur", label: "current"},
		}, "cur", func(string) bool { return true })
		ov.onDelete = func(id string) error { deleted = id; return nil }
		return ov
	}

	// Ctrl+D arms a confirmation; nothing is deleted yet.
	ov := newOverlay()
	deleted = ""
	ov.idx = 1 // s2
	ov.feedKey(Key{Kind: KeyCtrl, Byte: 4})
	if ov.pendingDeleteID != "s2" {
		t.Fatalf("pendingDeleteID = %q, want s2", ov.pendingDeleteID)
	}
	if deleted != "" {
		t.Fatal("must not delete before Enter confirmation")
	}
	// Enter confirms: onDelete fires and the row is removed.
	ov.feedKey(Key{Kind: KeyEnter})
	if deleted != "s2" {
		t.Fatalf("onDelete id = %q, want s2", deleted)
	}
	for _, it := range ov.items {
		if it.id == "s2" {
			t.Fatal("s2 should be gone from the list")
		}
	}

	// Esc cancels an armed delete without removing anything or closing.
	ov = newOverlay()
	deleted = ""
	ov.idx = 0 // s1
	ov.feedKey(Key{Kind: KeyCtrl, Byte: 4})
	_, _, cancel := ov.feedKey(Key{Kind: KeyEscape})
	if cancel {
		t.Fatal("Esc during confirm must cancel the prompt, not close the overlay")
	}
	if ov.pendingDeleteID != "" || deleted != "" {
		t.Fatal("Esc must cancel the pending delete")
	}

	// The active/current session cannot be deleted.
	ov = newOverlay()
	deleted = ""
	for i, it := range ov.items {
		if it.id == "cur" {
			ov.idx = i
		}
	}
	ov.feedKey(Key{Kind: KeyCtrl, Byte: 4})
	if ov.pendingDeleteID != "" {
		t.Fatal("must not arm deletion of the active session")
	}
	if ov.note == "" {
		t.Fatal("expected a note explaining the active session can't be deleted")
	}
}
