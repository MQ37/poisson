package tui

import "context"

// refreshProviderUsageLimits fetches fresh usage-limit data for whichever
// provider is currently active (both Refresh* calls are no-ops for the
// "other" provider) and re-syncs the header. Called by the background
// ticker in lifecycle.go on its own 5-minute schedule, and eagerly (via
// triggerUsageRefreshLocked, in a fresh goroutine) right after a provider
// switch — a freshly constructed provider's usage cache always starts
// empty, so without this the header would show nothing until the next
// scheduled tick, up to 5 minutes later.
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

// triggerUsageRefreshLocked kicks off refreshProviderUsageLimits in the
// background, without waiting for the ticker's own schedule. Caller must
// hold t.mu (the network call itself runs outside the lock, in a new
// goroutine); safe to call whether or not the provider actually changed — a
// same-provider call is just a normal TTL-gated cache hit.
func (t *TUI) triggerUsageRefreshLocked() {
	go t.refreshProviderUsageLimits(context.Background())
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
