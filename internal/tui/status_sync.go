package tui

import "context"

// refreshProviderUsageLimits fetches usage-limit data for whichever provider
// is currently active (both Refresh* calls are no-ops for the "other"
// provider), respecting each provider's own 5-minute TTL, and re-syncs the
// header. Called by the background ticker in lifecycle.go on its own
// 5-minute schedule — by then any existing cache is stale anyway, so there's
// no need to force past the TTL.
func (t *TUI) refreshProviderUsageLimits(ctx context.Context) {
	if t.agent == nil {
		return
	}
	t.agent.RefreshAnthropicUsageLimits(ctx)
	t.agent.RefreshOpenAIUsageLimits(ctx)
	t.mu.Lock()
	t.syncHeaderFromAgentLocked()
	t.mu.Unlock()
	t.dirty.markStatus()
}

// refreshProviderUsageLimitsForce is refreshProviderUsageLimits but bypasses
// each provider's TTL — used for an explicit, caller-requested refresh (see
// triggerUsageRefreshLocked) where the header must show guaranteed-current
// data right now, not whatever happens to already be cached.
func (t *TUI) refreshProviderUsageLimitsForce(ctx context.Context) {
	if t.agent == nil {
		return
	}
	t.agent.RefreshAnthropicUsageLimitsForce(ctx)
	t.agent.RefreshOpenAIUsageLimitsForce(ctx)
	t.mu.Lock()
	t.syncHeaderFromAgentLocked()
	t.mu.Unlock()
	t.dirty.markStatus()
}

// triggerUsageRefreshLocked kicks off a forced refreshProviderUsageLimitsForce
// in the background, without waiting for the ticker's own schedule, and — if
// the background ticker in lifecycle.go is running — resets its 5-minute
// schedule to start counting from now, so it doesn't also fire a redundant,
// near-duplicate refresh moments later on its own unrelated timeline. Caller
// must hold t.mu (the network call itself runs outside the lock, in a new
// goroutine); safe to call whether or not the provider actually changed —
// ForceUsageRefresh always hits the network regardless.
func (t *TUI) triggerUsageRefreshLocked() {
	go t.refreshProviderUsageLimitsForce(context.Background())
	select {
	case t.usageTickerReset <- struct{}{}:
	default:
	}
}

// syncHeaderFromAgentLocked refreshes the compact header from agent + store.
// Caller must hold t.mu.
func (t *TUI) syncHeaderFromAgentLocked() {
	if t.agent == nil {
		return
	}
	a := t.agent
	used, total := a.ContextTokens()
	t.status.ContextTokens = used
	t.status.ContextWindow = total
	t.status.ContextPct = a.ContextPercent()
	t.status.Effort = a.Effort()
	t.status.Model = modelLabel(a)
	// Pull the per-session tool/error counts straight from the agent so they
	// reset with the session (e.g. on /new) rather than lingering from status
	// events of the previous session.
	t.status.ToolCalls, t.status.ToolErrors = a.SessionToolStats()
	t.status.Turns = a.RunTurns()
	t.status.WarnContext = t.status.ContextPct > 75.0
	t.status.ApprovalMode = a.ApprovalMode()

	if tb, err := a.Store().GetSessionTokenBreakdown(a.SessionID()); err == nil {
		t.status.Cost = tb.TotalCost
		t.status.OutputTokens = tb.OutputTokens
		t.status.CacheRead = tb.CacheReadTokens
		t.status.CacheWrite = tb.CacheWriteTokens
		t.status.CallCount = tb.CallCount
	}

	t.status.AnthropicUsage = nil
	if u := a.AnthropicUsageLimits(); u != nil {
		t.status.AnthropicUsage = &AnthropicUsageView{
			FiveHourPct:   u.FiveHour.UtilizationPct,
			SevenDayPct:   u.SevenDay.UtilizationPct,
			ExtraEnabled:  u.ExtraUsageEnabled,
			ExtraUsed:     u.ExtraUsed,
			ExtraLimit:    u.ExtraLimit,
			ExtraCurrency: u.ExtraCurrency,
		}
	}

	t.status.CodexUsage = nil
	if u := a.OpenAIUsageLimits(); u != nil {
		t.status.CodexUsage = &CodexUsageView{
			UsedPercent:           u.UsedPercent,
			ResetCreditsAvailable: u.ResetCreditsAvailable,
		}
	}

	t.status.SessionID = a.SessionID()
	t.status.Title = ""
	if sess, err := a.Store().GetSession(a.SessionID()); err == nil {
		if sess.Title != nil && *sess.Title != "" {
			t.status.Title = *sess.Title
		}
	}
}
