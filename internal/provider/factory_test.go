package provider

import (
	"strings"
	"testing"

	"poisson/internal/auth"
	"poisson/internal/config"
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
	if got := DefaultModel("anthropic", cfg); got != "claude-opus-4-8" {
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
