package agent

import (
	"log"
	"strings"
	"time"

	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/store"
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

// estimateActiveContextTokens estimates the tokens the next request will send,
// following pi-mono's model: anchor on the exact usage of the last real request
// and add a char/4 estimate for only the messages appended since (tool results,
// a new user turn). This is accurate for the bulk (real usage) and never
// under-reports trailing growth. It falls back to a full char/4 estimate when
// there is no usable usage yet or a compaction has invalidated it. Overcounting
// is safe (compact a little early); undercounting risks a silent overflow.
func (a *Agent) estimateActiveContextTokens() int {
	msgs, _ := a.store.GetMessages(a.sessionID)
	sess, _ := a.store.GetSession(a.sessionID)
	fullEstimate := int(a.sysTokensEstimate.Load()) + a.summaryTokens(sess) + a.messagesTokens(msgs, -1)

	last, err := a.store.GetLastAPICall(a.sessionID)
	if err != nil || last.InputTokensUnknown {
		return fullEstimate
	}
	// Token counts are tokenizer-specific. Once provider or model changes, the
	// previous call no longer gives a trustworthy exact anchor for this request.
	if last.Provider != "" && (last.Provider != a.providerID() || last.Model != a.currentModel()) {
		return fullEstimate
	}
	// Legacy rows lack provider provenance; model mismatch is still enough to
	// invalidate their anchor after a model switch.
	if last.Provider == "" && last.Model != a.currentModel() {
		return fullEstimate
	}
	// A compaction after this call means its usage reflects the pre-compaction
	// (larger) prompt, not the active context.
	if sess != nil && sess.CompactedSeq > 0 && last.Seq <= sess.CompactedSeq {
		return fullEstimate
	}
	// last.TotalContextTokens covers system + tools + every message through the
	// assistant reply this call produced; add only what was appended after it.
	anchorSeq, ok := anchorMessageSeq(msgs, last.ID)
	if !ok {
		// Can't locate the anchor message — never under-report, take the larger.
		if anchor := last.TotalContextTokens(); anchor > fullEstimate {
			return anchor
		}
		return fullEstimate
	}
	return last.TotalContextTokens() + a.messagesTokens(msgs, anchorSeq)
}

// estimateMessagesTokens is the char/4 estimate of the compaction summary plus
// every active message (no real-usage anchor).
func (a *Agent) estimateMessagesTokens() int {
	msgs, _ := a.store.GetMessages(a.sessionID)
	sess, _ := a.store.GetSession(a.sessionID)
	return a.summaryTokens(sess) + a.messagesTokens(msgs, -1)
}

// summaryTokens is the char/4 estimate of a session's compaction summary.
func (a *Agent) summaryTokens(sess *store.Session) int {
	if sess != nil && sess.CompactionSummary != nil && *sess.CompactionSummary != "" {
		return a.EstimateTokens(*sess.CompactionSummary)
	}
	return 0
}

// messagesTokens sums the char/4 (+image) estimate of active messages with
// seq > afterSeq. Pass afterSeq = -1 to include all messages.
func (a *Agent) messagesTokens(msgs []store.Message, afterSeq int) int {
	total := 0
	for _, m := range msgs {
		if m.Seq <= afterSeq {
			continue
		}
		total += a.EstimateTokens(m.Content)
		// The stored content only holds each image's path (a few bytes), but the
		// downscaled image itself costs vision tokens; account for it flatly.
		total += strings.Count(m.Content, `"type":"image"`) * imageTokenEstimate
	}
	return total
}

// anchorMessageSeq returns the highest seq among active messages produced by the
// api_call with id callID — the point through which its usage already accounts.
func anchorMessageSeq(msgs []store.Message, callID string) (int, bool) {
	seq := -1
	for _, m := range msgs {
		if m.APICallID != nil && *m.APICallID == callID && m.Seq > seq {
			seq = m.Seq
		}
	}
	return seq, seq >= 0
}

// imageTokenEstimate is the rough vision-token cost of one downscaled
// (<=1024px) image; used only for the status-bar context estimate. Matches
// pi-mono's 4800-char/4 image budget and errs high (better to compact early).
const imageTokenEstimate = 1200

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
	return a.estimateActiveContextTokens() >= a.compactionLimit(window)
}

// compactionLimit is the token count at which auto-compaction should trigger:
// whichever comes first between the fractional threshold (threshold * window)
// and a fixed headroom reserve (window - reserveTokens). The reserve guarantees
// enough absolute room for the compaction round-trip regardless of window size
// (a percentage is too generous on huge windows and too tight on small ones).
// The reserve is ignored when it meets or exceeds the window (tiny windows).
func (a *Agent) compactionLimit(window int) int {
	threshold := a.config.Compaction.Threshold
	if threshold <= 0 {
		threshold = 0.85
	}
	limit := int(threshold * float64(window))
	if reserve := a.config.Compaction.ReserveTokens; reserve > 0 && reserve < window {
		if r := window - reserve; r < limit {
			limit = r
		}
	}
	return limit
}

// EstimateTokens returns a rough token count for a text string: len(text)/4.
func (a *Agent) EstimateTokens(text string) int {
	return len(text) / 4
}

// ContextWindow returns the context window size for the current model.
func (a *Agent) ContextWindow() int {
	model := a.currentModel()
	provID := a.providerID()

	if s, ok := provider.MergedModelSettings(a.config, provID, model); ok {
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

// sendInferenceSpeedEvent reports one completed streaming round's average
// output tokens/sec — usage.OutputTokens (exact, provider-reported) divided
// by wall-clock elapsed time since the request was sent. This is the same
// real number for every block the round produced (thinking, answer text,
// tool calls): none of poisson's providers (anthropic, openai, xai, ollama)
// break usage down more finely than one total for the whole response, so
// there is no finer-grained real figure to report per block instead.
//
// Always sends exactly one event per round, even when there's no reading to
// report (usage nil/zero output tokens, or the round was too short to measure
// meaningfully — see minInferenceSpeedElapsed) — then TokensPerSec is left at
// zero, which the TUI never displays (every render site gates on > 0). The
// TUI's scrollback.applyInferenceSpeed clears its "blocks from the round in
// flight" set as a side effect of handling this event; skipping the send
// entirely on the no-reading path would leave that set stuck, so the NEXT
// round's real reading would wrongly retag this round's blocks too.
func (a *Agent) sendInferenceSpeedEvent(usage *provider.Usage, roundStart time.Time) {
	ev := OutputEvent{Type: OutputInferenceSpeed}
	if usage != nil && usage.OutputTokens > 0 {
		if elapsed := time.Since(roundStart); elapsed >= minInferenceSpeedElapsed {
			ev.OutputTokens = usage.OutputTokens
			ev.TokensPerSec = float64(usage.OutputTokens) / elapsed.Seconds()
		}
	}
	a.sendEvent(ev)
}

// UpdateStatus sends a status OutputEvent to the output channel.
func (a *Agent) UpdateStatus() {
	used, total := a.ContextTokens()
	pct := a.ContextPercent()
	breakdown, err := a.store.GetSessionTokenBreakdown(a.sessionID)
	if err != nil {
		log.Printf("warning: status token breakdown: %v", err)
	}
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
