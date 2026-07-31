package tui

import "github.com/mq37/poisson/internal/store"

// windowTitleFor computes the window/tab title text for a session: "px -
// <title>" once the user has named it (/name), otherwise "px - <short
// session id>" so an unnamed session is still distinguishable from other
// poisson windows/tabs. Never a bare "px" with nothing to distinguish it,
// since sessionID is always non-empty once an agent is attached.
//
// pendingApproval prefixes "O " (outstanding) so a bash command waiting on
// the user's y/n shows up in a tab bar/window list even when that terminal
// tab isn't currently focused — otherwise an approval prompt sitting behind
// another window is easy to miss entirely. It takes priority over processing
// since an approval is the more actionable state (the agent is blocked on
// the user, not just busy).
//
// processing prefixes "~ " whenever the agent is actively working — a
// prompt in flight, a tool/subagent running, or a context compaction — so a
// backgrounded tab still signals "still going" vs "idle, safe to ignore" at
// a glance.
func windowTitleFor(title, sessionID string, pendingApproval, processing bool) string {
	label := title
	if label == "" {
		label = store.DisplaySessionID(sessionID)
	}
	base := "px"
	if label != "" {
		base = "px - " + label
	}
	switch {
	case pendingApproval:
		return "O " + base
	case processing:
		return "~ " + base
	default:
		return base
	}
}

// formatWindowTitle returns the OSC 0 escape sequence that sets both the
// terminal's window title and icon name.
func formatWindowTitle(title string) string {
	return "\x1b]0;" + title + "\a"
}

// updateWindowTitleLocked writes the window-title escape sequence if the
// computed title changed since the last call, so a redraw loop running at
// 30fps doesn't spam the terminal with an identical OSC sequence on every
// tick. Caller must hold t.mu and have already called syncHeaderFromAgentLocked
// (or otherwise populated t.status.Title/SessionID) for this frame.
func (t *TUI) updateWindowTitleLocked() {
	processing := needsSpinner(t.status.Thinking, t.activeTools, t.compacting.Load())
	want := windowTitleFor(t.status.Title, t.status.SessionID, t.approving.Load(), processing)
	if want == t.lastWindowTitle {
		return
	}
	t.lastWindowTitle = want
	t.writeRaw(formatWindowTitle(want))
}
