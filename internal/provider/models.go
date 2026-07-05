package provider

// ModelSettings holds per-model configuration that providers use to build
// requests correctly. Not all fields apply to all providers.
type ModelSettings struct {
	ContextWindow  int
	SupportsEffort bool
	EffortLevels   []string // e.g. ["low", "medium", "high", "xhigh", "max"]
}

// KnownModels is a registry of model metadata indexed by provider/model ID.
var KnownModels = map[string]ModelSettings{
	// Anthropic — only claude-opus-4-8
	"anthropic/claude-opus-4-8": {
		ContextWindow:  1000000,
		SupportsEffort: true,
		EffortLevels:   []string{"low", "medium", "high", "xhigh", "max"},
	},
	// xAI — only grok-build
	"xai/grok-build": {
		ContextWindow:  256000,
		SupportsEffort: false,
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
	},
	// kimi-k2.7-code:cloud — 256K context, Moonshot coding/agentic model.
	// Always operates in thinking mode (can't be disabled) and exposes no
	// configurable reasoning-effort level, so SupportsEffort is false.
	"ollama/kimi-k2.7-code:cloud": {
		ContextWindow:  256000,
		SupportsEffort: false,
	},
}

// GetModelSettings looks up model metadata by provider/model ID.
func GetModelSettings(providerID, modelID string) (ModelSettings, bool) {
	s, ok := KnownModels[providerID+"/"+modelID]
	return s, ok
}
