package agent

import (
	"strings"
	"time"

	"poisson/internal/provider"
	"poisson/internal/store"
)

// ContextPercent returns the context window usage as a percentage.
func (a *Agent) ContextPercent() float64 {
	used, total := a.ContextTokens()
	if total == 0 {
		return 0
	}
	return float64(used) / float64(total) * 100
}

// ContextTokens returns (used, total) for the status bar.
func (a *Agent) ContextTokens() (int, int) {
	total := a.ContextWindow()
	return a.estimateActiveContextTokens(), total
}

// estimateActiveContextTokens estimates tokens for the next request.
func (a *Agent) estimateActiveContextTokens() int {
	estimated := a.estimateMessagesTokens()
	last, err := a.store.GetLastAPICall(a.sessionID)
	if err != nil {
		return estimated
	}
	if last.InputTokensUnknown || last.InputTokens == 0 {
		return estimated
	}
	// After compaction active messages shrink; trust the lower estimate.
	if estimated < last.InputTokens {
		return estimated
	}
	return last.InputTokens
}

func (a *Agent) estimateMessagesTokens() int {
	total := 0
	if sess, err := a.store.GetSession(a.sessionID); err == nil && sess != nil &&
		sess.CompactionSummary != nil && *sess.CompactionSummary != "" {
		total += a.EstimateTokens(*sess.CompactionSummary)
	}
	msgs, err := a.store.GetMessages(a.sessionID)
	if err != nil {
		return total
	}
	for _, m := range msgs {
		total += a.EstimateTokens(m.Content)
		// The stored content only holds each image's path (a few bytes), but the
		// downscaled image itself costs vision tokens; account for it flatly.
		total += strings.Count(m.Content, `"type":"image"`) * imageTokenEstimate
	}
	return total
}

// imageTokenEstimate is the rough vision-token cost of one downscaled
// (<=1024px) image; used only for the status-bar context estimate.
const imageTokenEstimate = 800

// ShouldCompact returns true if estimated context exceeds the threshold.
func (a *Agent) ShouldCompact() bool {
	if a.config == nil {
		return false
	}
	if !a.compactBackoffUntil.IsZero() && time.Now().Before(a.compactBackoffUntil) {
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

	estimated := a.estimateMessagesTokens()
	if last, err := a.store.GetLastAPICall(a.sessionID); err == nil &&
		!last.InputTokensUnknown && last.InputTokens > estimated {
		estimated = last.InputTokens
	}

	return float64(estimated) >= threshold*float64(window)
}

// EstimateTokens returns a rough token count for a text string: len(text)/4.
func (a *Agent) EstimateTokens(text string) int {
	return len(text) / 4
}

// ContextWindow returns the context window size for the current model.
func (a *Agent) ContextWindow() int {
	model := a.currentModel()
	provID := a.provider.ID()

	if s, ok := provider.GetModelSettings(provID, model); ok {
		if s.ContextWindow > 0 {
			return s.ContextWindow
		}
	}

	if models, err := a.provider.Models(); err == nil {
		for _, m := range models {
			if m.ID == model || m.Name == model {
				if m.ContextWindow > 0 {
					return m.ContextWindow
				}
			}
		}
	}

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

// UpdateStatus sends a status OutputEvent to the output channel.
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

var _ = store.ErrNotFound