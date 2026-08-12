package provider

import (
	"testing"

	"github.com/mq37/poisson/internal/auth"
	"github.com/mq37/poisson/internal/config"
)

func customCfg() *config.Config {
	cfg := config.DefaultConfig()
	cfg.CustomProviders["bastion"] = &config.CustomProviderConfig{
		Type: "ollama", BaseURL: "http://bastion-host:11434", Model: "laguna-s-2.1:q4_K_M",
	}
	return cfg
}

// TestNewProviderCustom checks a custom provider constructs a
// CustomOllamaProvider carrying its own base_url/model/ID.
func TestNewProviderCustom(t *testing.T) {
	cfg := customCfg()
	p := NewProvider("bastion", auth.AuthStore{}, cfg)
	if p == nil {
		t.Fatal("NewProvider(bastion) = nil")
	}
	if p.ID() != "bastion" {
		t.Errorf("ID() = %q, want bastion", p.ID())
	}
	co, ok := p.(*CustomOllamaProvider)
	if !ok {
		t.Fatalf("type = %T, want *CustomOllamaProvider", p)
	}
	if co.baseURL != "http://bastion-host:11434" {
		t.Errorf("baseURL = %q", co.baseURL)
	}
}

// TestNewProviderCustomUnknownType checks a name absent from both the
// built-in registry and cfg.CustomProviders still returns nil.
func TestNewProviderCustomUnknownType(t *testing.T) {
	cfg := customCfg()
	if p := NewProvider("nope", auth.AuthStore{}, cfg); p != nil {
		t.Fatalf("NewProvider(nope) = %v, want nil", p)
	}
}

// TestNewProviderCustomNilConfig checks NewProvider degrades gracefully
// (nil, not panic) for a custom name when cfg is nil.
func TestNewProviderCustomNilConfig(t *testing.T) {
	if p := NewProvider("bastion", auth.AuthStore{}, nil); p != nil {
		t.Fatalf("NewProvider(bastion, nil cfg) = %v, want nil", p)
	}
}

// TestIsConfiguredCustomAlwaysTrue checks a custom (NeedsAuth=false)
// provider is always configured, same as ollama/llamacpp.
func TestIsConfiguredCustomAlwaysTrue(t *testing.T) {
	cfg := customCfg()
	if !IsConfigured("bastion", auth.AuthStore{}, cfg) {
		t.Error("expected bastion always configured")
	}
}

// TestDefaultModelCustom checks DefaultModel reads the instance's own
// configured model, with no built-in fallback string.
func TestDefaultModelCustom(t *testing.T) {
	cfg := customCfg()
	if got := DefaultModel("bastion", cfg); got != "laguna-s-2.1:q4_K_M" {
		t.Errorf("DefaultModel(bastion) = %q", got)
	}
}

// TestDefaultModelCustomNoModelConfigured checks an instance with no model
// set returns "" rather than panicking or inventing one.
func TestDefaultModelCustomNoModelConfigured(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.CustomProviders["bastion"] = &config.CustomProviderConfig{Type: "ollama", BaseURL: "http://bastion-host:11434"}
	if got := DefaultModel("bastion", cfg); got != "" {
		t.Errorf("DefaultModel(bastion) = %q, want empty", got)
	}
}

// TestBootstrapFromConfigCustomDefault checks a custom provider set as the
// session default provider bootstraps correctly end to end.
func TestBootstrapFromConfigCustomDefault(t *testing.T) {
	cfg := customCfg()
	cfg.Provider.Default = "bastion"
	prov, name, model, warn := BootstrapFromConfig(auth.AuthStore{}, cfg)
	if warn != "" {
		t.Fatalf("warn = %q, want empty", warn)
	}
	if name != "bastion" || model != "laguna-s-2.1:q4_K_M" {
		t.Fatalf("got %s/%s", name, model)
	}
	if prov == nil || prov.ID() != "bastion" {
		t.Fatalf("provider = %v", prov)
	}
}
