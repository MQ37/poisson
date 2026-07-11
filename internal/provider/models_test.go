package provider

import (
	"testing"

	"poisson/internal/config"
)

func TestCuratedModels(t *testing.T) {
	got := CuratedModels("ollama")
	byID := map[string]int{}
	for _, m := range got {
		byID[m.ID] = m.ContextWindow
	}
	for id, wantCtx := range map[string]int{
		"glm-5.2:cloud":        976000,
		"minimax-m3:cloud":     512000,
		"kimi-k2.7-code:cloud": 256000,
	} {
		if byID[id] != wantCtx {
			t.Errorf("CuratedModels(ollama)[%s] ctx=%d, want %d (all: %v)", id, byID[id], wantCtx, byID)
		}
	}
	// Sorted by ID.
	for i := 1; i < len(got); i++ {
		if got[i-1].ID > got[i].ID {
			t.Errorf("not sorted: %q before %q", got[i-1].ID, got[i].ID)
		}
	}
	// Uncurated provider falls through to empty.
	if len(CuratedModels("nope")) != 0 {
		t.Error("unknown provider should have no curated models")
	}
}

func TestMergedModelSettingsNoOverride(t *testing.T) {
	cfg := config.DefaultConfig()
	s, ok := MergedModelSettings(cfg, "anthropic", "claude-opus-4-8")
	if !ok {
		t.Fatal("expected known model to be found")
	}
	if s.ContextWindow != 1000000 || !s.AdaptiveThinking {
		t.Errorf("unexpected settings with no override: %+v", s)
	}
}

func TestMergedModelSettingsPartialOverride(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelOverrides["anthropic"] = map[string]config.ModelOverride{
		"claude-opus-4-8": {ContextWindow: 500000},
	}
	s, ok := MergedModelSettings(cfg, "anthropic", "claude-opus-4-8")
	if !ok {
		t.Fatal("expected known model to be found")
	}
	if s.ContextWindow != 500000 {
		t.Errorf("ContextWindow = %d, want 500000 (overridden)", s.ContextWindow)
	}
	// Untouched fields keep the built-in default.
	if !s.AdaptiveThinking || !s.Vision {
		t.Errorf("unrelated fields should keep built-in default: %+v", s)
	}
}

func TestMergedModelSettingsTeachesUnknownModel(t *testing.T) {
	cfg := config.DefaultConfig()
	vision := true
	cfg.ModelOverrides["xai"] = map[string]config.ModelOverride{
		"grok-5": {ContextWindow: 300000, Vision: &vision},
	}
	s, ok := MergedModelSettings(cfg, "xai", "grok-5")
	if !ok {
		t.Fatal("expected config-only model to be found (ok=true)")
	}
	if s.ContextWindow != 300000 || !s.Vision {
		t.Errorf("unexpected settings for config-only model: %+v", s)
	}
	if s.SupportsEffort {
		t.Error("effort_levels not set — SupportsEffort should stay false")
	}
}

func TestMergedModelSettingsEmptyEffortLevelsDisablesEffort(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelOverrides["ollama"] = map[string]config.ModelOverride{
		"glm-5.2:cloud": {EffortLevels: []string{}},
	}
	s, ok := MergedModelSettings(cfg, "ollama", "glm-5.2:cloud")
	if !ok {
		t.Fatal("expected known model to be found")
	}
	if s.SupportsEffort {
		t.Errorf("explicit empty effort_levels should disable effort support: %+v", s)
	}
}

func TestMergedModelSettingsUnknownWithoutOverride(t *testing.T) {
	cfg := config.DefaultConfig()
	if _, ok := MergedModelSettings(cfg, "anthropic", "totally-made-up"); ok {
		t.Error("expected ok=false for a model with neither a built-in entry nor an override")
	}
}

func TestMergedModelSettingsNilConfig(t *testing.T) {
	s, ok := MergedModelSettings(nil, "anthropic", "claude-opus-4-8")
	if !ok || s.ContextWindow != 1000000 {
		t.Errorf("nil cfg should fall back to the built-in entry: %+v ok=%v", s, ok)
	}
	if _, ok := MergedModelSettings(nil, "anthropic", "nope"); ok {
		t.Error("nil cfg + unknown model should be ok=false")
	}
}

func TestMergedCuratedModelsIncludesConfigOnlyModel(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelOverrides["ollama"] = map[string]config.ModelOverride{
		"qwen3-coder:cloud": {ContextWindow: 262144},
	}
	models := MergedCuratedModels(cfg, "ollama")
	var found bool
	for _, m := range models {
		if m.ID == "qwen3-coder:cloud" {
			found = true
			if m.ContextWindow != 262144 {
				t.Errorf("ContextWindow = %d, want 262144", m.ContextWindow)
			}
		}
	}
	if !found {
		t.Fatalf("config-only model missing from MergedCuratedModels: %v", models)
	}
	// The built-in models are still present too.
	if len(models) < len(CuratedModels("ollama")) {
		t.Errorf("expected at least the built-in models plus the new one, got %d", len(models))
	}
	// Sorted by ID.
	for i := 1; i < len(models); i++ {
		if models[i-1].ID > models[i].ID {
			t.Errorf("not sorted: %q before %q", models[i-1].ID, models[i].ID)
		}
	}
}

func TestMergedCuratedModelsOverridesKnownModelContextWindow(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelOverrides["anthropic"] = map[string]config.ModelOverride{
		"claude-opus-4-8": {ContextWindow: 42},
	}
	models := MergedCuratedModels(cfg, "anthropic")
	for _, m := range models {
		if m.ID == "claude-opus-4-8" && m.ContextWindow != 42 {
			t.Errorf("ContextWindow = %d, want 42 (overridden)", m.ContextWindow)
		}
	}
}
