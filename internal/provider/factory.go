package provider

import (
	"poisson/internal/auth"
	"poisson/internal/config"
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

// IsConfigured reports whether a provider has usable credentials. Ollama runs
// locally and needs none; the others need an OAuth session or an API key.
func IsConfigured(name string, a auth.AuthStore, cfg *config.Config) bool {
	switch name {
	case "ollama":
		return true
	case "anthropic":
		return auth.IsOAuth(a, "anthropic") || auth.GetAPIKey(a, "anthropic") != "" ||
			(cfg != nil && cfg.Anthropic.APIKey != "")
	case "xai", "openai":
		return auth.IsOAuth(a, name) || auth.GetAPIKey(a, name) != ""
	default:
		return false
	}
}

// IsConfiguredFromDisk loads auth from disk and reports whether the named
// provider has usable credentials.
func IsConfiguredFromDisk(name string, cfg *config.Config) bool {
	a, _ := auth.Load()
	return IsConfigured(name, a, cfg)
}

// NewProvider constructs a provider by name. Returns nil for unknown names.
func NewProvider(name string, a auth.AuthStore, cfg *config.Config) Provider {
	switch name {
	case "anthropic":
		return NewAnthropicProvider(a, cfg)
	case "ollama":
		baseURL := cfg.Ollama.BaseURL
		if baseURL == "" {
			baseURL = "http://localhost:11434"
		}
		return NewOllamaProvider(baseURL, cfg.Ollama.Model)
	case "xai":
		return NewXAIProvider(a, cfg)
	case "openai":
		return NewOpenAIProvider(a, cfg)
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
// fallbacks when the config field is empty.
func DefaultModel(provName string, cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	switch provName {
	case "anthropic":
		if m := cfg.Anthropic.Model; m != "" {
			return m
		}
		return "claude-opus-4-8"
	case "xai":
		if m := cfg.XAI.Model; m != "" {
			return m
		}
		return "grok-build"
	case "openai":
		if m := cfg.OpenAI.Model; m != "" {
			return m
		}
		return "gpt-5.5"
	case "ollama":
		if m := cfg.Ollama.Model; m != "" {
			return m
		}
		return "glm-5.2:cloud"
	default:
		return ""
	}
}

// BootstrapFromConfig resolves the default provider, constructs it, and
// returns the default model. warn is non-empty when a fallback was applied.
func BootstrapFromConfig(a auth.AuthStore, cfg *config.Config) (prov Provider, provName, model, warn string) {
	provName, warn = ResolveDefaultProvider(a, cfg)
	prov = NewProvider(provName, a, cfg)
	model = DefaultModel(provName, cfg)
	return prov, provName, model, warn
}
