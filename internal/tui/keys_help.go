package tui

import "strings"

// keybindingLines returns shared keyboard shortcut documentation.
func keybindingLines() []string {
	return []string{
		"Keys:",
		"  Tab          Input ↔ conversation focus",
		"  Enter        Send · Shift+Enter new line",
		"  PgUp/Dn      Scroll conversation",
		"  Shift+←/→    Previous/next prompt (conv focus)",
		"  Ctrl+F       Find in scrollback",
		"  Ctrl+P / .   Command palette",
		"  Ctrl+S       Session picker",
		"  Ctrl+M       Model picker",
		"  Ctrl+T       Toggle thinking block",
		"  Ctrl+E       Expand/collapse tool card",
		"  Ctrl+Y       Yank last assistant reply (OSC 52)",
		"  Ctrl+C       Cancel turn · twice to exit",
		"  Ctrl+D       Exit",
		"  Mouse wheel  Scroll (when no modal open)",
	}
}

func renderHelp() string {
	var b strings.Builder
	b.WriteString(strings.TrimRight(`Slash commands:
  /help        Show this help
  /quit        Exit Poisson
  /clear       Clear scrollback
  /new         Start a new session
  /resume <id> Resume a session
  /sessions    Session picker
  /search <q>  Search across sessions (no args: find in scrollback)
  /fork [seq]  Fork the current session
  /undo        Undo the last turn
  /compact     Compact context now
  /model <m>   Switch provider/model (no args: picker)
  /providers   Provider picker
  /effort <l>  Set thinking effort (low|medium|high|xhigh|max)
  /reload      Reload config and skills
  /cost        Show session cost
  /btw <q>     Side question in floating box`, "\n"))
	b.WriteString("\n\n")
	for _, ln := range keybindingLines() {
		b.WriteString(ln)
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}