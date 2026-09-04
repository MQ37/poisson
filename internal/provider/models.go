package provider

import (
	"fmt"
	"sort"
	"strings"

	"github.com/mq37/poisson/internal/config"
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
	// Description is a one-line, model-picking rationale surfaced to the LLM
	// itself (see FormatModelsForPrompt) — what this model is good for and
	// when to reach for it, not documentation for a human. Empty is fine
	// (FormatModelsForPrompt falls back to a placeholder).
	Description string
}

// KnownModels is a registry of model metadata indexed by provider/model ID.
var KnownModels = map[string]ModelSettings{
	// Anthropic — claude-fable-5-1, claude-opus-5, claude-sonnet-5, all
	// adaptive-thinking. claude-haiku-4-5 is deliberately absent here: it's
	// never a session model, only the fixed web-search helper (see
	// anthropic_web.go's anthropicWebModel).
	"anthropic/claude-fable-5-1": {
		ContextWindow:    1000000,
		SupportsEffort:   true,
		EffortLevels:     []string{"low", "medium", "high", "xhigh", "max"},
		Vision:           true,
		AdaptiveThinking: true,
		Description:      "Frontier-only: long-horizon agents, deepest reasoning, hardest research — cheaper than Fable 5 on cache-heavy agentic work (quarter-price cache reads). Expensive — reserve for tasks Opus 5 measurably can't handle.",
	},
	"anthropic/claude-opus-5": {
		ContextWindow:    1000000,
		SupportsEffort:   true,
		EffortLevels:     []string{"low", "medium", "high", "xhigh", "max"},
		Vision:           true,
		AdaptiveThinking: true,
		Description:      "Complex agentic coding, large refactors, multihour autonomous work, hard planning/design decisions. Default high effort; xhigh for the hardest coding/agentic tasks.",
	},
	"anthropic/claude-sonnet-5": {
		ContextWindow:    1000000,
		SupportsEffort:   true,
		EffortLevels:     []string{"low", "medium", "high", "xhigh", "max"},
		Vision:           true,
		AdaptiveThinking: true,
		Description:      "Best speed/cost balance — default choice for routine execution and most coding. xhigh for hard coding tasks needing more depth.",
	},
	// OpenAI — gpt-5.5 via the ChatGPT Codex subscription (Responses API).
	// Codex caps the subscription context at 400K; effort tops out at xhigh.
	"openai/gpt-5.5": {
		ContextWindow:  400000,
		SupportsEffort: true,
		EffortLevels:   []string{"low", "medium", "high", "xhigh"},
		Vision:         true,
		Description:    "OpenAI's prior-generation flagship via the ChatGPT Codex subscription — strong agentic coding and computer-use. Superseded by the GPT-5.6 family below; keep only for a session already pinned to it.",
	},
	// GPT-5.6 family — frontier (Sol), balanced (Terra), and cost-optimized
	// (Luna) tiers, full "none"-through-"max" effort range (unlike gpt-5.5's
	// narrower list). ContextWindow is 272,000 per GET /backend-api/codex/models
	// (Codex's own catalog endpoint), captured live via cc-sniff — not the 1.05M
	// this registry originally shipped with, which was never confirmed against
	// a real response and overshot the subscription's actual cap.
	"openai/gpt-5.6-sol": {
		ContextWindow:  272000,
		SupportsEffort: true,
		EffortLevels:   []string{"none", "low", "medium", "high", "xhigh", "max"},
		Vision:         true,
		Description:    "OpenAI's flagship GPT-5.6 tier: deepest reasoning, coding, and security work, with an ultra/parallel-subagent mode for the hardest tasks. Costliest of the three — use when Terra measurably isn't enough.",
	},
	"openai/gpt-5.6-terra": {
		ContextWindow:  272000,
		SupportsEffort: true,
		EffortLevels:   []string{"none", "low", "medium", "high", "xhigh", "max"},
		Vision:         true,
		Description:    "Balanced GPT-5.6 tier — GPT-5.5-class coding at roughly half the cost. Default choice for most agentic work; reach for Sol only when it measurably isn't enough.",
	},
	"openai/gpt-5.6-luna": {
		ContextWindow:  272000,
		SupportsEffort: true,
		EffortLevels:   []string{"none", "low", "medium", "high", "xhigh", "max"},
		Vision:         true,
		Description:    "Fastest, cheapest GPT-5.6 tier — quick or high-volume tasks where Terra's extra cost/latency isn't worth it.",
	},
	// xAI — grok-build, plus grok-4.5 (SpaceXAI's frontier coding/agentic
	// model, 500K context, configurable low/medium/high reasoning effort).
	"xai/grok-build": {
		ContextWindow:  256000,
		SupportsEffort: false,
		Vision:         true,
		Description:    "xAI's coding-agent model behind the Grok Build CLI — fast (100+ tok/s), always-on reasoning. 256K context trails larger-window models on big monorepos.",
	},
	"xai/grok-4.5": {
		ContextWindow:  500000,
		SupportsEffort: true,
		EffortLevels:   []string{"low", "medium", "high"},
		Vision:         true,
		Description:    "xAI's frontier coding/agentic model, trained jointly with Cursor — strong terminal use and multi-step software engineering across a 500K context.",
	},
	// OpenRouter — deepseek-v4-flash-0731 (13B active/284B total MoE),
	// confirmed live via GET https://openrouter.ai/api/v1/models: 1,048,576
	// context, 393,216 max completion tokens, reasoning_effort low|high|max
	// (no "medium" — deepseek's own API doesn't expose one), text-only (no
	// vision).
	"openrouter/deepseek/deepseek-v4-flash-0731": {
		ContextWindow:  1048576,
		SupportsEffort: true,
		EffortLevels:   []string{"low", "high", "max"},
		Description:    "DeepSeek's low-cost, high-throughput MoE — beats its own larger V4-Pro on published agentic benchmarks at a fraction of the price. 1M context; text-only.",
	},
	// Ollama — glm-5.2:cloud, minimax-m3:cloud, kimi-k2.7-code:cloud.
	"ollama/glm-5.2:cloud": {
		ContextWindow:  976000,
		SupportsEffort: true,
		EffortLevels:   []string{"high", "max"},
		Description:    "Zhipu's open-weight flagship MoE — near-1M context for full-repo refactors and cross-file dependency tracking, rivaling Opus-class coding at a fraction of the cost.",
	},
	// minimax-m3:cloud — 512K context, native interleaved thinking + tools.
	// Thinking is always-on (streamed as reasoning_content); Ollama exposes no
	// configurable reasoning-effort level, so SupportsEffort is false.
	"ollama/minimax-m3:cloud": {
		ContextWindow:  512000,
		SupportsEffort: false,
		Vision:         true,
		Description:    "MiniMax's open-weight model — native multimodal (image/video) input and long-context agentic coding via sparse attention; thinking is always on.",
	},
	// kimi-k2.7-code:cloud — 256K context, Moonshot coding/agentic model.
	// Always operates in thinking mode (can't be disabled) and exposes no
	// configurable reasoning-effort level, so SupportsEffort is false.
	"ollama/kimi-k2.7-code:cloud": {
		ContextWindow:  256000,
		SupportsEffort: false,
		Vision:         true,
		Description:    "Moonshot's coding-focused open MoE — long-horizon multi-file engineering and strong MCP tool-use; always-on thinking, fewer reasoning tokens than its K2.6 base.",
	},
	// llamacpp — local llama-server instances (see workdir/alpaca), served
	// straight from the HF-cached GGUF. Context windows match model card
	// metadata; neither exposes a reasoning-effort knob over the wire.
	"llamacpp/unsloth/Laguna-S-2.1-GGUF": {
		ContextWindow:  262144,
		SupportsEffort: false,
		Description:    "Poolside's open-weight agentic-coding MoE (118B total/8B active) — local coding via unsloth's llama.cpp GGUF build, served at 256K context here (up to 1M natively).",
	},
	"llamacpp/poolside/Laguna-XS-2.1-GGUF": {
		ContextWindow:  262144,
		SupportsEffort: false,
		Description:    "Poolside's Laguna family, XS tier — smaller/faster than Laguna S 2.1 for lighter local runs, same open agentic-coding architecture.",
	},
	"llamacpp/unsloth/Qwen3.6-27B-MTP-GGUF": {
		ContextWindow:  262144,
		SupportsEffort: false,
		Vision:         true,
		Description:    "Alibaba's dense 27B multimodal model — beats their own larger Qwen3.5 MoE on coding benchmarks; light enough for a single 24GB GPU quantized.",
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

// FormatModelsForPrompt lists providerID's curated models (including any
// config.toml-only additions) with their descriptions and effort support,
// one per line, for injection into a prompt that needs to choose one (see
// the subagent tool's Description()). Every model is listed, including
// whichever one is currently active — "here's everything on this provider"
// reads clearer than a silently-shortened list. Empty string when the
// provider has no curated entries at all.
func FormatModelsForPrompt(cfg *config.Config, providerID string) string {
	models := MergedCuratedModels(cfg, providerID)
	if len(models) == 0 {
		return ""
	}
	var b strings.Builder
	for _, m := range models {
		desc := "(no description)"
		effort := "no effort override"
		if s, ok := MergedModelSettings(cfg, providerID, m.ID); ok {
			if s.Description != "" {
				desc = s.Description
			}
			if s.SupportsEffort && len(s.EffortLevels) > 0 {
				effort = strings.Join(s.EffortLevels, "/")
			}
		}
		fmt.Fprintf(&b, "- %s: %s (effort: %s)\n", m.ID, desc, effort)
	}
	return strings.TrimRight(b.String(), "\n")
}
