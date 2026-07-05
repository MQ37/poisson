package provider

import (
	"sort"
	"strings"
)

// ModelSettings holds per-model configuration that providers use to build
// requests correctly. Not all fields apply to all providers.
type ModelSettings struct {
	ContextWindow  int
	SupportsEffort bool
	EffortLevels   []string // e.g. ["low", "medium", "high", "xhigh", "max"]
	Vision         bool     // accepts image input
}

// KnownModels is a registry of model metadata indexed by provider/model ID.
var KnownModels = map[string]ModelSettings{
	// Anthropic — only claude-opus-4-8
	"anthropic/claude-opus-4-8": {
		ContextWindow:  1000000,
		SupportsEffort: true,
		EffortLevels:   []string{"low", "medium", "high", "xhigh", "max"},
		Vision:         true,
	},
	// OpenAI — gpt-5.5 via the ChatGPT Codex subscription (Responses API).
	// Codex caps the subscription context at 400K; effort tops out at xhigh.
	"openai/gpt-5.5": {
		ContextWindow:  400000,
		SupportsEffort: true,
		EffortLevels:   []string{"low", "medium", "high", "xhigh"},
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

// GetModelSettings looks up model metadata by provider/model ID.
func GetModelSettings(providerID, modelID string) (ModelSettings, bool) {
	s, ok := KnownModels[providerID+"/"+modelID]
	return s, ok
}

// CuratedModels returns the KnownModels entries for a provider as Model values,
// sorted by ID. This is the curated menu the model picker shows — the source of
// truth for which models are offered, independent of whatever a provider's live
// listing (e.g. Ollama's /api/tags) happens to report. Empty when the provider
// has no curated entries.
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
