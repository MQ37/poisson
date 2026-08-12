package provider

import (
	"github.com/mq37/poisson/internal/auth"
	"github.com/mq37/poisson/internal/config"
)

// ResolveDefaultProvider picks the provider name from config, applying
// fallbacks when credentials are missing.
func ResolveDefaultProvider(a auth.AuthStore, cfg *config.Config) (name string, warn string) {
	if cfg == nil {
		return "ollama", ""
	}
	name = cfg.Provider.Default
	if name == "" {
		name = "ollama"
	}
	if !IsConfigured(name, a, cfg) {
		warn = "no " + name + " credentials found, using ollama"
		name = "ollama"
	}
	return name, warn
}

// IsConfigured reports whether a provider has usable credentials. Providers
// that run locally (NeedsAuth false, e.g. ollama, llamacpp) need none; the
// rest need an OAuth session, an auth.json API key, or (anthropic only) a
// config.toml api_key.
func IsConfigured(name string, a auth.AuthStore, cfg *config.Config) bool {
	meta, ok := config.ResolveProviderMeta(name, cfg)
	if !ok {
		return false
	}
	if !meta.NeedsAuth {
		return true
	}
	if auth.IsOAuth(a, name) || auth.GetAPIKey(a, name) != "" {
		return true
	}
	return meta.APIKey != nil && cfg != nil && meta.APIKey(cfg) != ""
}

// IsConfiguredFromDisk loads auth from disk and reports whether the named
// provider has usable credentials.
func IsConfiguredFromDisk(name string, cfg *config.Config) bool {
	a, _ := auth.Load()
	return IsConfigured(name, a, cfg)
}

// providerConstructors maps provider ID to its constructor. Every ID in
// config.Providers must have one here — TestProviderRegistryParity in
// factory_test.go fails loudly if a provider is added to the config
// registry without a matching constructor.
var providerConstructors = map[string]func(auth.AuthStore, *config.Config) Provider{
	"anthropic": func(a auth.AuthStore, cfg *config.Config) Provider {
		return NewAnthropicProvider(a, cfg)
	},
	"ollama": func(_ auth.AuthStore, cfg *config.Config) Provider {
		baseURL := cfg.Ollama.BaseURL
		if baseURL == "" {
			baseURL = "http://localhost:11434"
		}
		return NewOllamaProvider(baseURL, cfg.Ollama.Model)
	},
	"llamacpp": func(_ auth.AuthStore, cfg *config.Config) Provider {
		baseURL := cfg.LlamaCpp.BaseURL
		if baseURL == "" {
			baseURL = "http://localhost:11212"
		}
		return NewLlamaCppProvider(baseURL, cfg.LlamaCpp.Model)
	},
	"xai": func(a auth.AuthStore, cfg *config.Config) Provider {
		return NewXAIProvider(a, cfg)
	},
	"openai": func(a auth.AuthStore, cfg *config.Config) Provider {
		return NewOpenAIProvider(a, cfg)
	},
}

// NewProvider constructs a provider by name: a built-in first (the static
// providerConstructors map), then a cfg.CustomProviders instance. Returns
// nil for a name neither registry knows about.
func NewProvider(name string, a auth.AuthStore, cfg *config.Config) Provider {
	if ctor, ok := providerConstructors[name]; ok {
		return ctor(a, cfg)
	}
	return newCustomProvider(name, cfg)
}

// newCustomProvider constructs a provider from cfg.CustomProviders, dispatching
// on its Type. Only "ollama" exists today — config.parseCustomProviders
// already rejects any other type at load time, so the default case here is
// unreachable through normal config parsing; it's still an explicit nil
// rather than a panic for a *config.Config built by hand (e.g. tests).
func newCustomProvider(name string, cfg *config.Config) Provider {
	if cfg == nil {
		return nil
	}
	cp, ok := cfg.CustomProviders[name]
	if !ok {
		return nil
	}
	switch cp.Type {
	case "ollama":
		return NewCustomOllamaProvider(name, cp.BaseURL, cp.Model)
	default:
		return nil
	}
}

// NewProviderFromDisk loads auth from disk and constructs a provider by name.
func NewProviderFromDisk(name string, cfg *config.Config) Provider {
	a, _ := auth.Load()
	return NewProvider(name, a, cfg)
}

// DefaultModel returns the configured model for a provider, with built-in
// fallbacks (config.Providers) when the config field is empty. A custom
// provider has no built-in fallback string, so this returns "" until the
// user sets [custom_providers.<name>].model or switches via /model.
func DefaultModel(provName string, cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	meta, ok := config.ResolveProviderMeta(provName, cfg)
	if !ok {
		return ""
	}
	if m := *meta.Model(cfg); m != "" {
		return m
	}
	return meta.DefaultModel
}

// BootstrapFromConfig resolves the default provider, constructs it, and
// returns the default model. warn is non-empty when a fallback was applied.
func BootstrapFromConfig(a auth.AuthStore, cfg *config.Config) (prov Provider, provName, model, warn string) {
	provName, warn = ResolveDefaultProvider(a, cfg)
	prov = NewProvider(provName, a, cfg)
	model = DefaultModel(provName, cfg)
	return prov, provName, model, warn
}
