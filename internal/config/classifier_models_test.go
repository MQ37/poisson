package config

import "testing"

// TestProviderClassifierKeys covers the maintainable form: each provider
// declares its own classifier next to its own model, instead of a single
// string that can only ever name one provider.
func TestProviderClassifierKeys(t *testing.T) {
	cfg, err := mapToConfig(mustParse(t, `
[anthropic]
model = "claude-opus-5"
classifier = "claude-sonnet-5"

[xai]
classifier = "grok-build-0.1"

[classifier]
model = "fallback-model"
`))
	if err != nil {
		t.Fatalf("mapToConfig: %v", err)
	}
	if cfg.Anthropic.Model != "claude-opus-5" {
		t.Errorf("anthropic.model = %q, want claude-opus-5", cfg.Anthropic.Model)
	}
	if got := cfg.Classifier.Models["anthropic"]; got != "claude-sonnet-5" {
		t.Errorf("anthropic classifier = %q, want claude-sonnet-5", got)
	}
	if got := cfg.Classifier.Models["xai"]; got != "grok-build-0.1" {
		t.Errorf("xai classifier = %q, want grok-build-0.1", got)
	}
	if cfg.Classifier.Model != "fallback-model" {
		t.Errorf("classifier.model = %q, want the fallback to survive", cfg.Classifier.Model)
	}
	if _, ok := cfg.Classifier.Models["ollama"]; ok {
		t.Error("ollama declared no classifier; it must not get an entry")
	}
}

// TestProviderClassifierStripsRedundantPrefix accepts the redundant
// "anthropic/claude-sonnet-5" spelling but stores the bare model, since the
// table already names the provider.
func TestProviderClassifierStripsRedundantPrefix(t *testing.T) {
	cfg, err := mapToConfig(mustParse(t, `
[anthropic]
classifier = "anthropic/claude-sonnet-5"
`))
	if err != nil {
		t.Fatalf("mapToConfig: %v", err)
	}
	if got := cfg.Classifier.Models["anthropic"]; got != "claude-sonnet-5" {
		t.Errorf("anthropic classifier = %q, want claude-sonnet-5", got)
	}
}

// TestProviderClassifierRejectsMismatchedProvider fails loudly instead of
// silently picking a side: under [xai], "anthropic/claude-sonnet-5" cannot
// mean anything sensible.
func TestProviderClassifierRejectsMismatchedProvider(t *testing.T) {
	_, err := mapToConfig(mustParse(t, `
[xai]
classifier = "anthropic/claude-sonnet-5"
`))
	if err == nil {
		t.Fatal("want an error for a provider-mismatched classifier entry")
	}
}

// TestProviderClassifierKeepsSlashInModelName covers HF-style model names:
// llamacpp's own default is "unsloth/Laguna-S-2.1-GGUF", whose leading segment
// is not a provider id and must not be mistaken for one.
func TestProviderClassifierKeepsSlashInModelName(t *testing.T) {
	cfg, err := mapToConfig(mustParse(t, `
[llamacpp]
classifier = "unsloth/Laguna-S-2.1-GGUF"
`))
	if err != nil {
		t.Fatalf("mapToConfig: %v", err)
	}
	if got := cfg.Classifier.Models["llamacpp"]; got != "unsloth/Laguna-S-2.1-GGUF" {
		t.Errorf("llamacpp classifier = %q, want the full model name", got)
	}
}

func mustParse(t *testing.T, toml string) map[string]interface{} {
	t.Helper()
	m, err := Parse(toml)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return m
}
