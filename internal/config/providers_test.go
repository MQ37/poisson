package config

import (
	"strings"
	"testing"
)

// TestProviderMetaByID checks lookup hits and misses.
func TestProviderMetaByID(t *testing.T) {
	for _, id := range []string{"anthropic", "ollama", "llamacpp", "xai", "openai"} {
		if _, ok := ProviderMetaByID(id); !ok {
			t.Errorf("ProviderMetaByID(%q) not found", id)
		}
	}
	if _, ok := ProviderMetaByID("bogus"); ok {
		t.Error("ProviderMetaByID(bogus) should not be found")
	}
}

// TestProviderIDsMatchesRegistry checks ProviderIDs mirrors Providers 1:1, in order.
func TestProviderIDsMatchesRegistry(t *testing.T) {
	ids := ProviderIDs()
	if len(ids) != len(Providers) {
		t.Fatalf("len(ProviderIDs())=%d, len(Providers)=%d", len(ids), len(Providers))
	}
	for i, p := range Providers {
		if ids[i] != p.ID {
			t.Errorf("ProviderIDs()[%d] = %q, want %q", i, ids[i], p.ID)
		}
	}
}

// TestProvidersEveryEntryValid guards against a copy-paste registry entry
// missing required fields (empty ID/Desc/DefaultModel, nil Model accessor).
func TestProvidersEveryEntryValid(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range Providers {
		if p.ID == "" {
			t.Fatal("provider with empty ID")
		}
		if seen[p.ID] {
			t.Fatalf("duplicate provider ID %q", p.ID)
		}
		seen[p.ID] = true
		if p.Desc == "" {
			t.Errorf("%s: empty Desc", p.ID)
		}
		if p.DefaultModel == "" {
			t.Errorf("%s: empty DefaultModel", p.ID)
		}
		if p.Model == nil {
			t.Fatalf("%s: nil Model accessor", p.ID)
		}
		cfg := &Config{}
		*p.Model(cfg) = "probe"
		if got := *p.Model(cfg); got != "probe" {
			t.Errorf("%s: Model accessor round-trip failed, got %q", p.ID, got)
		}
	}
}

// TestDefaultConfigSeedsEveryProviderModel checks defaultConfig() sets each
// provider's Model field from the registry, not a hand-copied literal.
func TestDefaultConfigSeedsEveryProviderModel(t *testing.T) {
	cfg := defaultConfig()
	for _, p := range Providers {
		if got := *p.Model(cfg); got != p.DefaultModel {
			t.Errorf("%s: default model = %q, want %q", p.ID, got, p.DefaultModel)
		}
	}
}

// TestSetProviderModelUnknownListsRegistry checks the error message reflects
// the live registry instead of a hand-copied string.
func TestSetProviderModelUnknownListsRegistry(t *testing.T) {
	cfg := defaultConfig()
	err := setProviderModel(cfg, "bogus", "x")
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
	for _, id := range ProviderIDs() {
		if !strings.Contains(err.Error(), id) {
			t.Errorf("error %q missing provider id %q", err, id)
		}
	}
}

// TestSetProviderModelKnown checks every registered provider can be targeted.
func TestSetProviderModelKnown(t *testing.T) {
	for _, p := range Providers {
		cfg := defaultConfig()
		if err := setProviderModel(cfg, p.ID, "custom-model"); err != nil {
			t.Errorf("%s: setProviderModel: %v", p.ID, err)
		}
		if got := *p.Model(cfg); got != "custom-model" {
			t.Errorf("%s: model = %q, want custom-model", p.ID, got)
		}
	}
}

// TestDefaultConfigTomlListsEveryProvider guards the TOML doc comment against
// drifting from the registry (this is exactly the bug that caused llamacpp to
// go missing from the picker on the first pass — see TODO).
func TestDefaultConfigTomlListsEveryProvider(t *testing.T) {
	toml := defaultConfigToml()
	for _, id := range ProviderIDs() {
		if !strings.Contains(toml, id) {
			t.Errorf("default config.toml template missing provider %q", id)
		}
	}
}

// TestDefaultPricingCoversEveryLocalProvider checks the wildcard entry exists
// for providers with no fixed per-model price list (local, free to run).
func TestDefaultPricingCoversEveryLocalProvider(t *testing.T) {
	pricing := defaultPricing()
	for _, p := range Providers {
		if p.NeedsAuth {
			continue
		}
		if _, ok := pricing[p.ID]; !ok {
			t.Errorf("%s: no default pricing entry (want at least a %q wildcard)", p.ID, "*")
		}
	}
}
