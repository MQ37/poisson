package tui

// windowTitleIDLen caps how much of an unnamed session's ID appears in the
// window title — long enough to tell tabs/windows apart, short enough to fit
// comfortably in a terminal tab bar.
const windowTitleIDLen = 8

// windowTitleFor computes the window/tab title text for a session: "px -
// <title>" once the user has named it (/name), otherwise "px - <short
// session id>" so an unnamed session is still distinguishable from other
// poisson windows/tabs. Never a bare "px" with nothing to distinguish it,
// since sessionID is always non-empty once an agent is attached.
//
// pendingApproval prefixes "O " (outstanding) so a bash command waiting on
// the user's y/n shows up in a tab bar/window list even when that terminal
// tab isn't currently focused — otherwise an approval prompt sitting behind
// another window is easy to miss entirely.
func windowTitleFor(title, sessionID string, pendingApproval bool) string {
	label := title
	if label == "" {
		label = sessionID
		if len(label) > windowTitleIDLen {
			label = label[:windowTitleIDLen]
		}
	}
	base := "px"
	if label != "" {
		base = "px - " + label
	}
	if pendingApproval {
		return "O " + base
	}
	return base
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
	want := windowTitleFor(t.status.Title, t.status.SessionID, t.approving.Load())
	if want == t.lastWindowTitle {
		return
	}
	t.lastWindowTitle = want
	t.writeRaw(formatWindowTitle(want))
}
