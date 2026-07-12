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
		"  Ctrl+S       Session picker (Ctrl+D deletes the selected session)",
		"  Ctrl+M       Model picker",
		"  Ctrl+L       Effort picker",
		"  Ctrl+T       Toggle thinking block",
		"  Ctrl+E       Expand/collapse tool card",
		"  Ctrl+G       Expedite running subagents (finish ASAP)",
		"  Ctrl+C       Cancel turn · twice to exit",
		"  Ctrl+D       Exit",
		"  Mouse wheel  Scroll (when no modal open)",
		"  Click+drag   Select text (auto-scrolls at the edge)",
		"  Ctrl+Y       Copy selected text",
	}
}

func renderHelp() string {
	var b strings.Builder
	b.WriteString(strings.TrimRight(`Slash commands:
  /help        Show this help
  /quit        Exit Poisson
  /clear       Clear scrollback
  /name <t>    Set session display title
  /new         Start a new session
  /resume <id> Resume a session
  /sessions    Session picker
  /search <q>  Search across sessions (no args: find in scrollback)
  /compact     Compact context now
  /model <m>   Switch provider/model (no args: picker)
  /providers   Provider picker
  /effort [l]  Effort picker (or set level incl. max)
  /reload      Reload config and skills
  /cost        Show session cost
  /status      Session info + loaded context files & skills
  /btw <q>     Side question in floating box`, "\n"))
	b.WriteString("\n\n")
	for _, ln := range keybindingLines() {
		b.WriteString(ln)
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}
