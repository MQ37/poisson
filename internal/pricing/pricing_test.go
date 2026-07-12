package pricing

import (
	"testing"

	"poisson/internal/config"
)

func TestLookupBuiltIn(t *testing.T) {
	r, ok := Lookup(nil, "anthropic", "claude-opus-4-8")
	if !ok || r.InputPerMTok != 5.0 || r.OutputPerMTok != 25.0 {
		t.Fatalf("opus = %+v ok=%v", r, ok)
	}
	r, ok = Lookup(nil, "ollama", "qwen3-coder:30b")
	if !ok || r.InputPerMTok != 0 {
		t.Fatalf("ollama wildcard = %+v", r)
	}
	_, ok = Lookup(nil, "unknown", "nope")
	if ok {
		t.Fatal("expected no pricing for unknown")
	}
	r, ok = Lookup(nil, "openai", "gpt-5.5")
	if !ok || r.InputPerMTok != 5.0 || r.OutputPerMTok != 30.0 || r.CacheReadPerMTok != 0.5 {
		t.Fatalf("gpt-5.5 = %+v ok=%v", r, ok)
	}
}

func TestLookupConfigOverride(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Pricing["anthropic"]["claude-opus-4-8"] = config.Pricing{
		InputPerMTok: 3.0, OutputPerMTok: 15.0,
	}
	r, ok := Lookup(cfg, "anthropic", "claude-opus-4-8")
	if !ok || r.InputPerMTok != 3.0 {
		t.Fatalf("override = %+v", r)
	}
}

func TestComputeCost(t *testing.T) {
	cfg := config.DefaultConfig()
	cost := ComputeCost(cfg, "anthropic", "claude-opus-4-8", 1_000_000, 1_000_000, 0, 0)
	if cost != 30.0 {
		t.Fatalf("cost = %v, want 30", cost)
	}
	cost = ComputeCost(cfg, "xai", "grok-build", 1_000_000, 1_000_000, 0, 0)
	if cost != 3.0 {
		t.Fatalf("grok cost = %v", cost)
	}
	if ComputeCost(cfg, "unknown", "nope", 1_000_000, 1_000_000, 0, 0) != 0 {
		t.Fatal("unknown should be free")
	}
}