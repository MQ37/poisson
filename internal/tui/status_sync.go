package tui

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

	if tb, err := a.Store().GetSessionTokenBreakdown(a.SessionID()); err == nil {
		t.status.Cost = tb.TotalCost
		t.status.OutputTokens = tb.OutputTokens
		t.status.CacheRead = tb.CacheReadTokens
		t.status.CacheWrite = tb.CacheWriteTokens
		t.status.CallCount = tb.CallCount
	}

	t.status.SessionID = a.SessionID()
	t.status.Title = ""
	if sess, err := a.Store().GetSession(a.SessionID()); err == nil {
		if sess.Title != nil && *sess.Title != "" {
			t.status.Title = *sess.Title
		}
	}
}