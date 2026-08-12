package config

import (
	"strings"
	"testing"
)

// TestLoadCustomProviderOllama checks a [custom_providers.<name>] table with
// type = "ollama" parses into cfg.CustomProviders, independent of the
// built-in Ollama slot.
func TestLoadCustomProviderOllama(t *testing.T) {
	writeTempConfig(t, `
[custom_providers.bastion]
type = "ollama"
base_url = "http://bastion-host:11434"
model = "laguna-s-2.1:q4_K_M"
`)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cp, ok := cfg.CustomProviders["bastion"]
	if !ok {
		t.Fatal("expected custom provider \"bastion\"")
	}
	if cp.Type != "ollama" {
		t.Errorf("Type = %q, want ollama", cp.Type)
	}
	if cp.BaseURL != "http://bastion-host:11434" {
		t.Errorf("BaseURL = %q", cp.BaseURL)
	}
	if cp.Model != "laguna-s-2.1:q4_K_M" {
		t.Errorf("Model = %q", cp.Model)
	}
	// Built-in ollama slot must be untouched.
	if cfg.Ollama.BaseURL != "http://localhost:11434" {
		t.Errorf("built-in Ollama.BaseURL leaked custom value: %q", cfg.Ollama.BaseURL)
	}
}

// TestLoadCustomProviderModelOptional checks model = "" is allowed (user
// picks via /model later).
func TestLoadCustomProviderModelOptional(t *testing.T) {
	writeTempConfig(t, `
[custom_providers.bastion]
type = "ollama"
base_url = "http://bastion-host:11434"
`)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.CustomProviders["bastion"].Model != "" {
		t.Errorf("Model = %q, want empty", cfg.CustomProviders["bastion"].Model)
	}
}

// TestLoadCustomProviderMultiple checks two independent instances coexist.
func TestLoadCustomProviderMultiple(t *testing.T) {
	writeTempConfig(t, `
[custom_providers.bastion]
type = "ollama"
base_url = "http://bastion-host:11434"

[custom_providers.workstation]
type = "ollama"
base_url = "http://192.168.1.50:11434"
model = "qwen3-coder:30b"
`)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.CustomProviders) != 2 {
		t.Fatalf("len(CustomProviders) = %d, want 2", len(cfg.CustomProviders))
	}
	if cfg.CustomProviders["workstation"].Model != "qwen3-coder:30b" {
		t.Errorf("workstation.Model = %q", cfg.CustomProviders["workstation"].Model)
	}
}

// TestLoadCustomProviderMissingType rejects a table with no type.
func TestLoadCustomProviderMissingType(t *testing.T) {
	writeTempConfig(t, `
[custom_providers.bastion]
base_url = "http://bastion-host:11434"
`)
	if _, err := Load(); err == nil {
		t.Fatal("expected error for missing type")
	} else if !strings.Contains(err.Error(), "type") {
		t.Errorf("error = %q, want it to mention type", err)
	}
}

// TestLoadCustomProviderUnsupportedType rejects any type other than ollama.
func TestLoadCustomProviderUnsupportedType(t *testing.T) {
	writeTempConfig(t, `
[custom_providers.bastion]
type = "openai-compatible"
base_url = "http://bastion-host:11434"
`)
	if _, err := Load(); err == nil {
		t.Fatal("expected error for unsupported type")
	} else if !strings.Contains(err.Error(), "openai-compatible") {
		t.Errorf("error = %q, want it to mention the bad type", err)
	}
}

// TestLoadCustomProviderMissingBaseURL rejects a table with no base_url.
func TestLoadCustomProviderMissingBaseURL(t *testing.T) {
	writeTempConfig(t, `
[custom_providers.bastion]
type = "ollama"
`)
	if _, err := Load(); err == nil {
		t.Fatal("expected error for missing base_url")
	} else if !strings.Contains(err.Error(), "base_url") {
		t.Errorf("error = %q, want it to mention base_url", err)
	}
}

// TestLoadCustomProviderNameCollidesWithBuiltin rejects a custom name that
// shadows a built-in provider ID — otherwise ResolveProviderMeta's
// built-in-first lookup order would silently make the custom entry
// unreachable.
func TestLoadCustomProviderNameCollidesWithBuiltin(t *testing.T) {
	for _, name := range []string{"ollama", "anthropic", "xai", "openai", "llamacpp"} {
		writeTempConfig(t, `
[custom_providers.`+name+`]
type = "ollama"
base_url = "http://bastion-host:11434"
`)
		if _, err := Load(); err == nil {
			t.Errorf("name %q: expected collision error", name)
		} else if !strings.Contains(err.Error(), "built-in") {
			t.Errorf("name %q: error = %q, want it to mention built-in collision", name, err)
		}
	}
}

// TestLoadCustomProviderNameWithSlashRejected rejects a name containing '/'
// — it would be ambiguous with the "provider/model" splitting used
// everywhere (px -p, /model, session resolution).
func TestLoadCustomProviderNameWithSlashRejected(t *testing.T) {
	writeTempConfig(t, `
[custom_providers."bastion/ollama"]
type = "ollama"
base_url = "http://bastion-host:11434"
`)
	if _, err := Load(); err == nil {
		t.Fatal("expected error for name containing '/'")
	} else if !strings.Contains(err.Error(), "/") {
		t.Errorf("error = %q, want it to mention the slash", err)
	}
}

// TestLoadCustomProviderTopLevelModelOneLiner checks the documented
// top-level `model = "<provider>/<model>"` shorthand works for a custom
// provider exactly like a built-in one — parseCustomProviders must run
// before this knob is resolved, and setProviderModel must resolve through
// cfg.CustomProviders, not just the built-in registry.
func TestLoadCustomProviderTopLevelModelOneLiner(t *testing.T) {
	writeTempConfig(t, `
model = "bastion/laguna-s-2.1:q4_K_M"

[custom_providers.bastion]
type = "ollama"
base_url = "http://bastion-host:11434"
`)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Provider.Default != "bastion" {
		t.Errorf("Provider.Default = %q, want bastion", cfg.Provider.Default)
	}
	if cfg.CustomProviders["bastion"].Model != "laguna-s-2.1:q4_K_M" {
		t.Errorf("CustomProviders[bastion].Model = %q", cfg.CustomProviders["bastion"].Model)
	}
}

// TestLoadCustomProviderEmptyTableRejected guards against a table with
// neither type nor base_url producing a silently-empty entry.
func TestLoadCustomProviderEmptyTableRejected(t *testing.T) {
	writeTempConfig(t, `
[custom_providers.bastion]
`)
	if _, err := Load(); err == nil {
		t.Fatal("expected error for empty custom provider table")
	}
}
