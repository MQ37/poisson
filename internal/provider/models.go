package provider

import (
	"sort"
	"strings"

	"poisson/internal/config"
)

// ModelSettings holds per-model configuration that providers use to build
// requests correctly. Not all fields apply to all providers.
type ModelSettings struct {
	ContextWindow  int
	SupportsEffort bool
	EffortLevels   []string // e.g. ["low", "medium", "high", "xhigh", "max"]
	Vision         bool     // accepts image input
	// AdaptiveThinking selects Anthropic's adaptive reasoning: the model decides
	// how much to think, driven by output_config.effort, instead of a fixed
	// budget_tokens. Matches what the real Claude Code client sends.
	AdaptiveThinking bool
}

// KnownModels is a registry of model metadata indexed by provider/model ID.
var KnownModels = map[string]ModelSettings{
	// Anthropic — claude-opus-4-8 and claude-sonnet-5, both adaptive-thinking.
	"anthropic/claude-opus-4-8": {
		ContextWindow:    1000000,
		SupportsEffort:   true,
		EffortLevels:     []string{"low", "medium", "high", "xhigh", "max"},
		Vision:           true,
		AdaptiveThinking: true,
	},
	"anthropic/claude-sonnet-5": {
		ContextWindow:    1000000,
		SupportsEffort:   true,
		EffortLevels:     []string{"low", "medium", "high", "xhigh", "max"},
		Vision:           true,
		AdaptiveThinking: true,
	},
	// OpenAI — gpt-5.5 via the ChatGPT Codex subscription (Responses API).
	// Codex caps the subscription context at 400K; effort tops out at xhigh.
	"openai/gpt-5.5": {
		ContextWindow:  400000,
		SupportsEffort: true,
		EffortLevels:   []string{"low", "medium", "high", "xhigh"},
		Vision:         true,
	},
	// GPT-5.6 family — frontier (Sol), balanced (Terra), and cost-optimized
	// (Luna) tiers, all sharing the same 1.05M context window and full
	// "none"-through-"max" effort range (unlike gpt-5.5's narrower list).
	"openai/gpt-5.6-sol": {
		ContextWindow:  1050000,
		SupportsEffort: true,
		EffortLevels:   []string{"none", "low", "medium", "high", "xhigh", "max"},
		Vision:         true,
	},
	"openai/gpt-5.6-terra": {
		ContextWindow:  1050000,
		SupportsEffort: true,
		EffortLevels:   []string{"none", "low", "medium", "high", "xhigh", "max"},
		Vision:         true,
	},
	"openai/gpt-5.6-luna": {
		ContextWindow:  1050000,
		SupportsEffort: true,
		EffortLevels:   []string{"none", "low", "medium", "high", "xhigh", "max"},
		Vision:         true,
	},
	// xAI — only grok-build
	"xai/grok-build": {
		ContextWindow:  256000,
		SupportsEffort: false,
		Vision:         true,
	},
	// Ollama — glm-5.2:cloud, minimax-m3:cloud, kimi-k2.7-code:cloud.
	"ollama/glm-5.2:cloud": {
		ContextWindow:  976000,
		SupportsEffort: true,
		EffortLevels:   []string{"high", "max"},
	},
	// minimax-m3:cloud — 512K context, native interleaved thinking + tools.
	// Thinking is always-on (streamed as reasoning_content); Ollama exposes no
	// configurable reasoning-effort level, so SupportsEffort is false.
	"ollama/minimax-m3:cloud": {
		ContextWindow:  512000,
		SupportsEffort: false,
		Vision:         true,
	},
	// kimi-k2.7-code:cloud — 256K context, Moonshot coding/agentic model.
	// Always operates in thinking mode (can't be disabled) and exposes no
	// configurable reasoning-effort level, so SupportsEffort is false.
	"ollama/kimi-k2.7-code:cloud": {
		ContextWindow:  256000,
		SupportsEffort: false,
		Vision:         true,
	},
}

// GetModelSettings looks up model metadata by provider/model ID in the
// built-in registry only. Most callers want MergedModelSettings instead,
// which also applies config.toml's [models.*] overrides.
func GetModelSettings(providerID, modelID string) (ModelSettings, bool) {
	s, ok := KnownModels[providerID+"/"+modelID]
	return s, ok
}

// MergedModelSettings returns ModelSettings for providerID/modelID, starting
// from the built-in KnownModels entry (if any) and layering cfg's
// [models.<provider>.<model>] override on top. A model unlisted in
// KnownModels but present in the override still returns ok=true — this is
// how config.toml teaches Poisson about a model the code has never heard of.
// ok=false only when neither source has an entry.
func MergedModelSettings(cfg *config.Config, providerID, modelID string) (ModelSettings, bool) {
	base, known := GetModelSettings(providerID, modelID)
	override, hasOverride := modelOverride(cfg, providerID, modelID)
	if !known && !hasOverride {
		return ModelSettings{}, false
	}
	if !hasOverride {
		return base, true
	}
	if override.ContextWindow > 0 {
		base.ContextWindow = override.ContextWindow
	}
	if override.EffortLevels != nil {
		base.EffortLevels = override.EffortLevels
		base.SupportsEffort = len(override.EffortLevels) > 0
	}
	if override.Vision != nil {
		base.Vision = *override.Vision
	}
	if override.AdaptiveThinking != nil {
		base.AdaptiveThinking = *override.AdaptiveThinking
	}
	return base, true
}

func modelOverride(cfg *config.Config, providerID, modelID string) (config.ModelOverride, bool) {
	if cfg == nil {
		return config.ModelOverride{}, false
	}
	mo, ok := cfg.ModelOverrides[providerID][modelID]
	return mo, ok
}

// CuratedModels returns the KnownModels entries for a provider as Model values,
// sorted by ID. This is the curated menu the model picker shows — the source of
// truth for which models are offered, independent of whatever a provider's live
// listing (e.g. Ollama's /api/tags) happens to report. Empty when the provider
// has no curated entries. Most callers want MergedCuratedModels instead, which
// also surfaces config.toml-only models.
func CuratedModels(providerID string) []Model {
	prefix := providerID + "/"
	var out []Model
	for key, s := range KnownModels {
		id, ok := strings.CutPrefix(key, prefix)
		if !ok {
			continue
		}
		out = append(out, Model{ID: id, Name: id, ContextWindow: s.ContextWindow})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// MergedCuratedModels is CuratedModels plus any models that only exist in
// cfg's [models.<provider>.*] overrides (a model the code has never heard
// of, taught entirely via config.toml) — so those show up in the /model
// picker too, not just when set directly in config.
func MergedCuratedModels(cfg *config.Config, providerID string) []Model {
	out := CuratedModels(providerID)
	seen := make(map[string]bool, len(out))
	for i := range out {
		seen[out[i].ID] = true
		if s, ok := MergedModelSettings(cfg, providerID, out[i].ID); ok {
			out[i].ContextWindow = s.ContextWindow
		}
	}
	if cfg != nil {
		for modelID := range cfg.ModelOverrides[providerID] {
			if seen[modelID] {
				continue
			}
			s, _ := MergedModelSettings(cfg, providerID, modelID)
			out = append(out, Model{ID: modelID, Name: modelID, ContextWindow: s.ContextWindow})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
