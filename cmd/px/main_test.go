package main

import (
	"testing"

	"poisson/internal/config"
	"poisson/internal/store"
)

func TestResolvePrintRuntimeRestoresExistingSession(t *testing.T) {
	cfg := config.DefaultConfig()
	sess := &store.Session{ID: "s1", Provider: "xai", Model: "grok-build"}

	providerID, model, err := resolvePrintRuntime("", sess, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if providerID != "xai" || model != "grok-build" {
		t.Fatalf("runtime = %s/%s, want xai/grok-build", providerID, model)
	}
}

func TestResolvePrintRuntimeOverrideReplacesPair(t *testing.T) {
	cfg := config.DefaultConfig()
	sess := &store.Session{ID: "s1", Provider: "xai", Model: "grok-build"}

	providerID, model, err := resolvePrintRuntime("openai/gpt-5.5", sess, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if providerID != "openai" || model != "gpt-5.5" {
		t.Fatalf("runtime = %s/%s, want openai/gpt-5.5", providerID, model)
	}
}

func TestResolvePrintRuntimeBareProviderUsesDefault(t *testing.T) {
	cfg := config.DefaultConfig()

	providerID, model, err := resolvePrintRuntime("anthropic", nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if providerID != "anthropic" || model != cfg.Anthropic.Model {
		t.Fatalf("runtime = %s/%s, want anthropic/%s", providerID, model, cfg.Anthropic.Model)
	}
}

func TestResolvePrintRuntimeRejectsIncompletePair(t *testing.T) {
	if _, _, err := resolvePrintRuntime("ollama/", nil, config.DefaultConfig()); err == nil {
		t.Fatal("expected incomplete provider/model error")
	}
}
