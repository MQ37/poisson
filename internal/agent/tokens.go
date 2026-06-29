package agent

import (
	"poisson/internal/provider"
	"poisson/internal/store"
)

// ContextPercent returns the context window usage as a percentage:
// last_api_call.input_tokens / context_window * 100.
func (a *Agent) ContextPercent() float64 {
	last, err := a.store.GetLastAPICall(a.sessionID)
	if err != nil {
		return 0
	}
	window := a.ContextWindow()
	if window == 0 {
		return 0
	}
	return float64(last.InputTokens) / float64(window) * 100
}

// ContextTokens returns (used, total) from the last api_call and the
// provider's context window for the current model.
func (a *Agent) ContextTokens() (int, int) {
	total := a.ContextWindow()
	last, err := a.store.GetLastAPICall(a.sessionID)
	if err != nil {
		return 0, total
	}
	return last.InputTokens, total
}

// ShouldCompact returns true if the estimated token usage for the next
// request exceeds the configured threshold fraction of the context window:
//
//	last_input_tokens + estimated_new_tokens >= threshold * context_window
//
// estimated_new_tokens is derived from the tool result texts appended in the
// current iteration (pendingResults).
func (a *Agent) ShouldCompact() bool {
	if a.config == nil {
		return false
	}
	last, err := a.store.GetLastAPICall(a.sessionID)
	if err != nil {
		return false
	}
	window := a.ContextWindow()
	if window == 0 {
		return false
	}
	threshold := a.config.Compaction.Threshold
	if threshold <= 0 {
		threshold = 0.85
	}

	estimatedNew := 0
	for _, text := range a.pendingResults {
		estimatedNew += a.EstimateTokens(text)
	}

	return float64(last.InputTokens+estimatedNew) >= threshold*float64(window)
}

// EstimateTokens returns a rough token count for a text string: len(text)/4.
// This is only used for compaction triggering, never stored.
func (a *Agent) EstimateTokens(text string) int {
	return len(text) / 4
}

// ContextWindow returns the context window size for the current model.
// Checks KnownModels registry first, then the provider's model list,
// then falls back to a provider-specific default.
func (a *Agent) ContextWindow() int {
	model := a.currentModel()
	provID := a.provider.ID()

	// Try the known models registry first (has accurate context windows).
	if s, ok := provider.GetModelSettings(provID, model); ok {
		if s.ContextWindow > 0 {
			return s.ContextWindow
		}
	}

	// Try the provider's model list.
	if models, err := a.provider.Models(); err == nil {
		for _, m := range models {
			if m.ID == model || m.Name == model {
				if m.ContextWindow > 0 {
					return m.ContextWindow
				}
			}
		}
	}

	// Provider-specific fallback.
	switch provID {
	case "anthropic":
		return 200000
	case "xai":
		return 131072
	case "ollama":
		return 8192
	default:
		return 8192
	}
}

// UpdateStatus sends a status OutputEvent to the output channel with the
// current context %, token usage, cumulative cost, and model name.
func (a *Agent) UpdateStatus() {
	used, total := a.ContextTokens()
	pct := a.ContextPercent()
	breakdown, _ := a.store.GetSessionTokenBreakdown(a.sessionID)
	model := a.currentModel()

	a.sendEvent(OutputEvent{
		Type:             OutputStatus,
		ContextPct:       pct,
		ContextTokens:    used,
		ContextWindow:    total,
		Cost:             breakdown.TotalCost,
		Model:            model,
		OutputTokens:     breakdown.OutputTokens,
		CacheReadTokens:  breakdown.CacheReadTokens,
		CacheWriteTokens: breakdown.CacheWriteTokens,
		CallCount:        breakdown.CallCount,
		ToolCalls:        a.sessionToolCalls,
		ToolErrors:       a.sessionToolErrors,
		Effort:           a.effort,
	})
}

// Compile-time assertion that store is used.
var _ = store.ErrNotFound
