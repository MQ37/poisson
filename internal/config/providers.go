package config

import "sort"

// ProviderMeta is the single source of truth entry for one provider: its ID,
// a short description (shown in the /providers picker), whether it needs
// credentials, its built-in default model, and an accessor into that
// provider's own Model config field.
//
// internal/provider/factory.go (IsConfigured/NewProvider/DefaultModel),
// internal/tui/overlay_picker.go (the /providers picker), and this package's
// own defaultConfig/setProviderModel/TOML template all read Providers
// instead of each keeping an independent copy of "the list of providers" —
// see TODO "Provider registry: scattered, no single source of truth".
//
// Adding a provider: add one entry here, add its own config struct + wire
// any extra knobs (base_url, api_key, ...) into mapToConfig, and add its
// constructor to provider.providerConstructors — factory_test.go's
// TestProviderRegistryParity fails if that last step is forgotten.
type ProviderMeta struct {
	ID           string
	Desc         string                // e.g. "Claude API/OAuth" — shown in the /providers picker
	NeedsAuth    bool                  // false: runs locally, always "configured" (ollama, llamacpp)
	DefaultModel string                // built-in fallback when the config field is empty
	Model        func(*Config) *string // pointer to this provider's own Model field
	APIKey       func(*Config) string  // optional extra api_key override; nil if the provider has none
}

// Providers is the ordered registry of every provider Poisson knows about,
// in /providers picker and TOML template display order.
var Providers = []ProviderMeta{
	{
		ID:           "anthropic",
		Desc:         "Claude API/OAuth",
		NeedsAuth:    true,
		DefaultModel: "claude-opus-5",
		Model:        func(c *Config) *string { return &c.Anthropic.Model },
		APIKey:       func(c *Config) string { return c.Anthropic.APIKey },
	},
	{
		ID:           "ollama",
		Desc:         "local models",
		NeedsAuth:    false,
		DefaultModel: "glm-5.2:cloud",
		Model:        func(c *Config) *string { return &c.Ollama.Model },
	},
	{
		ID:           "llamacpp",
		Desc:         "local llama-server",
		NeedsAuth:    false,
		DefaultModel: "unsloth/Laguna-S-2.1-GGUF",
		Model:        func(c *Config) *string { return &c.LlamaCpp.Model },
	},
	{
		ID:           "xai",
		Desc:         "Grok OAuth",
		NeedsAuth:    true,
		DefaultModel: "grok-build",
		Model:        func(c *Config) *string { return &c.XAI.Model },
	},
	{
		ID:        "openai",
		Desc:      "GPT ChatGPT subscription",
		NeedsAuth: true,
		// Terra is the balanced tier of the current GPT-5.6 family — the same
		// role gpt-5.5 used to play as the default.
		DefaultModel: "gpt-5.6-terra",
		Model:        func(c *Config) *string { return &c.OpenAI.Model },
	},
}

// ProviderMetaByID looks up one provider's metadata by ID.
func ProviderMetaByID(id string) (ProviderMeta, bool) {
	for _, p := range Providers {
		if p.ID == id {
			return p, true
		}
	}
	return ProviderMeta{}, false
}

// ProviderIDs returns every known provider ID, in registry order.
func ProviderIDs() []string {
	ids := make([]string, len(Providers))
	for i, p := range Providers {
		ids[i] = p.ID
	}
	return ids
}

// ResolveProviderMeta looks up a provider's metadata by ID, checking the
// built-in registry (ProviderMetaByID) first and cfg's user-defined
// [custom_providers.<name>] instances second. Use this — not the plain
// cfg-less ProviderMetaByID — anywhere the ID might name a custom provider:
// provider.NewProvider/IsConfigured/DefaultModel, the /providers and /model
// pickers, px -p and subagent provider validation. Load rejects a custom
// name that collides with a built-in one, so the built-in-first order here
// never actually shadows a real custom entry.
//
// A custom provider is always NeedsAuth=false (v1 supports type="ollama"
// only, which runs unauthenticated) and has no built-in DefaultModel
// fallback — DefaultModel returns "" until the user sets one, in
// [custom_providers.<name>] or via /model.
func ResolveProviderMeta(id string, cfg *Config) (ProviderMeta, bool) {
	if p, ok := ProviderMetaByID(id); ok {
		return p, true
	}
	if cfg == nil {
		return ProviderMeta{}, false
	}
	cp, ok := cfg.CustomProviders[id]
	if !ok {
		return ProviderMeta{}, false
	}
	return ProviderMeta{
		ID:        id,
		Desc:      cp.Type + " @ " + cp.BaseURL,
		NeedsAuth: false,
		Model:     func(*Config) *string { return &cp.Model },
	}, true
}

// AllProviderMeta returns every provider Poisson knows about for this
// config: the built-in registry (Providers, in its fixed order) followed by
// every [custom_providers.*] instance, sorted by name for a deterministic
// /providers picker. cfg may be nil (built-ins only, matches Providers).
func AllProviderMeta(cfg *Config) []ProviderMeta {
	out := append([]ProviderMeta(nil), Providers...)
	if cfg == nil {
		return out
	}
	names := make([]string, 0, len(cfg.CustomProviders))
	for name := range cfg.CustomProviders {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if meta, ok := ResolveProviderMeta(name, cfg); ok {
			out = append(out, meta)
		}
	}
	return out
}
