package provider

import (
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/auth"
	"github.com/mq37/poisson/internal/config"
	"github.com/mq37/poisson/internal/testutil"
)

func TestResolveDefaultProvider_AnthropicFallback(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Provider.Default = "anthropic"
	name, warn := ResolveDefaultProvider(auth.AuthStore{}, cfg)
	if name != "ollama" {
		t.Fatalf("name = %q, want ollama", name)
	}
	if !strings.Contains(warn, "anthropic") {
		t.Fatalf("warn = %q, want anthropic fallback message", warn)
	}
}

func TestResolveDefaultProvider_ConfiguredAnthropic(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Provider.Default = "anthropic"
	cfg.Anthropic.APIKey = "sk-test"
	name, warn := ResolveDefaultProvider(auth.AuthStore{}, cfg)
	if name != "anthropic" {
		t.Fatalf("name = %q, want anthropic", name)
	}
	if warn != "" {
		t.Fatalf("warn = %q, want empty", warn)
	}
}

func TestDefaultModel_Fallbacks(t *testing.T) {
	cfg := config.DefaultConfig()
	if got := DefaultModel("anthropic", cfg); got != "claude-opus-5" {
		t.Fatalf("anthropic default = %q", got)
	}
	if got := DefaultModel("xai", cfg); got != "grok-build" {
		t.Fatalf("xai default = %q", got)
	}
	if got := DefaultModel("ollama", cfg); got != "glm-5.2:cloud" {
		t.Fatalf("ollama default = %q", got)
	}
}

func TestNewProvider_Unknown(t *testing.T) {
	if p := NewProvider("unknown", auth.AuthStore{}, config.DefaultConfig()); p != nil {
		t.Fatal("expected nil for unknown provider")
	}
}

// TestProviderRegistryParity checks providerConstructors has exactly one
// entry per config.Providers ID, and neither list has an orphan the other
// doesn't know about. This is the regression test for the "llamacpp missing
// from /providers" bug: adding a provider to config.Providers without
// wiring its constructor here now fails CI instead of silently degrading.
func TestProviderRegistryParity(t *testing.T) {
	want := map[string]bool{}
	for _, p := range config.Providers {
		want[p.ID] = true
	}
	if len(providerConstructors) != len(want) {
		t.Fatalf("providerConstructors has %d entries, config.Providers has %d", len(providerConstructors), len(want))
	}
	for id := range want {
		if _, ok := providerConstructors[id]; !ok {
			t.Errorf("provider %q in config.Providers has no constructor in providerConstructors", id)
		}
	}
	for id := range providerConstructors {
		if !want[id] {
			t.Errorf("providerConstructors has %q, missing from config.Providers", id)
		}
	}
}

// TestNewProviderConstructsEveryRegisteredProvider checks NewProvider
// returns a non-nil, correctly-ID'd provider for every registry entry.
func TestNewProviderConstructsEveryRegisteredProvider(t *testing.T) {
	cfg := config.DefaultConfig()
	for _, p := range config.Providers {
		got := NewProvider(p.ID, auth.AuthStore{}, cfg)
		if got == nil {
			t.Errorf("NewProvider(%q) = nil", p.ID)
			continue
		}
		if got.ID() != p.ID {
			t.Errorf("NewProvider(%q).ID() = %q", p.ID, got.ID())
		}
	}
}

// TestIsConfiguredLocalProvidersAlwaysTrue checks NeedsAuth=false providers
// report configured regardless of auth state.
func TestIsConfiguredLocalProvidersAlwaysTrue(t *testing.T) {
	for _, p := range config.Providers {
		if p.NeedsAuth {
			continue
		}
		if !IsConfigured(p.ID, auth.AuthStore{}, config.DefaultConfig()) {
			t.Errorf("%s: NeedsAuth=false provider should always be configured", p.ID)
		}
	}
}

// TestIsConfiguredAuthProvidersNeedCreds checks NeedsAuth=true providers are
// reported unconfigured with no credentials present anywhere.
func TestIsConfiguredAuthProvidersNeedCreds(t *testing.T) {
	for _, p := range config.Providers {
		if !p.NeedsAuth {
			continue
		}
		if IsConfigured(p.ID, auth.AuthStore{}, config.DefaultConfig()) {
			t.Errorf("%s: NeedsAuth=true provider should not be configured with no credentials", p.ID)
		}
	}
}

func TestDefaultModelUnknownProvider(t *testing.T) {
	if got := DefaultModel("bogus", config.DefaultConfig()); got != "" {
		t.Fatalf("DefaultModel(bogus) = %q, want empty", got)
	}
}

// TestIsConfiguredFromDisk_TogglesWithSavedAuth confirms IsConfiguredFromDisk
// actually reads through to auth.json: an auth-requiring provider reports
// unconfigured before any credentials are saved, and configured immediately
// after auth.Save persists a usable entry.
func TestIsConfiguredFromDisk_TogglesWithSavedAuth(t *testing.T) {
	testutil.TempHome(t)
	cfg := config.DefaultConfig()

	if IsConfiguredFromDisk("anthropic", cfg) {
		t.Fatal("expected anthropic unconfigured before any auth.Save")
	}
	if err := auth.Save(auth.AuthStore{"anthropic": {Type: "api_key", Key: "sk-ant-test"}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !IsConfiguredFromDisk("anthropic", cfg) {
		t.Fatal("expected anthropic configured after auth.Save with an api_key entry")
	}
}

// TestIsConfiguredFromDisk_OllamaNeedsNoAuth confirms a NeedsAuth=false
// provider is always configured, with or without anything in auth.json.
func TestIsConfiguredFromDisk_OllamaNeedsNoAuth(t *testing.T) {
	testutil.TempHome(t)
	cfg := config.DefaultConfig()

	if !IsConfiguredFromDisk("ollama", cfg) {
		t.Fatal("expected ollama always configured (NeedsAuth=false) before any auth.Save")
	}
	if err := auth.Save(auth.AuthStore{"anthropic": {Type: "api_key", Key: "unrelated"}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !IsConfiguredFromDisk("ollama", cfg) {
		t.Fatal("expected ollama still configured after an unrelated Save")
	}
}

// TestNewProviderFromDisk_ConstructsForKnownProviders confirms
// NewProviderFromDisk actually loads auth from disk and constructs a
// correctly-ID'd provider, for both an auth-requiring provider and a local
// one — through the disk-loading entry point rather than NewProvider's own
// already-tested in-memory path.
func TestNewProviderFromDisk_ConstructsForKnownProviders(t *testing.T) {
	testutil.TempHome(t)
	cfg := config.DefaultConfig()
	if err := auth.Save(auth.AuthStore{"anthropic": {Type: "api_key", Key: "sk-ant-test"}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	for _, id := range []string{"anthropic", "ollama"} {
		p := NewProviderFromDisk(id, cfg)
		if p == nil {
			t.Fatalf("NewProviderFromDisk(%q) = nil", id)
		}
		if p.ID() != id {
			t.Errorf("NewProviderFromDisk(%q).ID() = %q", id, p.ID())
		}
	}
}

func TestBootstrapFromConfig(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Provider.Default = "ollama"
	prov, name, model, warn := BootstrapFromConfig(auth.AuthStore{}, cfg)
	if warn != "" {
		t.Fatalf("warn = %q, want empty", warn)
	}
	if name != "ollama" || model != "glm-5.2:cloud" {
		t.Fatalf("got %s/%s", name, model)
	}
	if prov == nil || prov.ID() != "ollama" {
		t.Fatalf("provider = %v", prov)
	}
}
