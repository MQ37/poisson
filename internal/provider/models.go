package provider

// ModelSettings holds per-model configuration that providers use to build
// requests correctly. Not all fields apply to all providers.
type ModelSettings struct {
	ContextWindow  int
	MaxOutput      int
	SupportsEffort bool
	EffortLevels   []string // e.g. ["low", "medium", "high", "xhigh", "max"]
	InputPrice     float64  // USD per 1M tokens
	OutputPrice    float64
}

// KnownModels is a registry of model metadata indexed by provider/model ID.
var KnownModels = map[string]ModelSettings{
	// Anthropic — only claude-opus-4-8
	"anthropic/claude-opus-4-8": {
		ContextWindow:  1000000,
		MaxOutput:      128000,
		SupportsEffort: true,
		EffortLevels:   []string{"low", "medium", "high", "xhigh", "max"},
		InputPrice:     5.0,
		OutputPrice:    25.0,
	},
	// xAI — only grok-build
	"xai/grok-build": {
		ContextWindow:  256000,
		MaxOutput:      0,
		SupportsEffort: false,
		InputPrice:     1.0,
		OutputPrice:    2.0,
	},
	// Ollama — only glm-5.2:cloud and minimax-m3:cloud
	"ollama/glm-5.2:cloud": {
		ContextWindow:  976000,
		MaxOutput:      0,
		SupportsEffort: true,
		EffortLevels:   []string{"high", "max"},
		InputPrice:     0,
		OutputPrice:    0,
	},
	"ollama/minimax-m3:cloud": {
		ContextWindow:  512000,
		MaxOutput:      0,
		SupportsEffort: true,
		EffortLevels:   []string{"low", "medium", "high"},
		InputPrice:     0,
		OutputPrice:    0,
	},
}

// GetModelSettings looks up model metadata by provider/model ID.
func GetModelSettings(providerID, modelID string) (ModelSettings, bool) {
	s, ok := KnownModels[providerID+"/"+modelID]
	return s, ok
}
