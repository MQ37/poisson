package tui

import (
	"strings"

	"github.com/mq37/poisson/internal/guard"
)

func approvalBarPrefix() string { return fgRed + "█ " + reset }
func bashPromptPrefix() string  { return fgYellow + bold + "$ " + reset }
func bashSafeStyle() string     { return fgYellow + bold }
func bashDangerStyle() string   { return fgRed + bold }

// formatBashCommandHighlight styles a bash command like bash-guard: safe tokens
// in toolTitle yellow, dangerous tokens/patterns in error red (both bold).
func formatBashCommandHighlight(command string) string {
	var b strings.Builder
	for _, span := range guard.HighlightSpans(command) {
		if span.Danger {
			b.WriteString(bashDangerStyle())
		} else {
			b.WriteString(bashSafeStyle())
		}
		b.WriteString(span.Text)
	}
	b.WriteString(reset)
	return b.String()
}

// approvalCommandLines wraps a bash command for the approval overlay with the
// red █ bar, $ prompt, and per-token danger highlighting.
func approvalCommandLines(command string, width int) []string {
	return wrapHighlightedBashMultiline(command, width, true)
}

// wrapHighlightedBashMultiline wraps a possibly multiline bash command.
func wrapHighlightedBashMultiline(command string, width int, withApprovalBar bool) []string {
	if width < 1 {
		width = 1
	}
	command = strings.ReplaceAll(command, "\r\n", "\n")
	paragraphs := strings.Split(command, "\n")

	prompt := bashPromptPrefix()
	bar := ""
	if withApprovalBar {
		bar = approvalBarPrefix()
	}
	firstPrefix := bar + prompt
	nextPrefix := prompt
	if withApprovalBar {
		nextPrefix = bar + prompt
	}
	firstPad := strings.Repeat(" ", visibleWidth(firstPrefix))
	nextPad := strings.Repeat(" ", visibleWidth(nextPrefix))

	var out []string
	for pi, part := range paragraphs {
		prefix := firstPrefix
		contPad := firstPad
		if pi > 0 {
			prefix = nextPrefix
			contPad = nextPad
		}
		inner := width - visibleWidth(prefix)
		if inner < 1 {
			inner = 1
		}
		highlighted := formatBashCommandHighlight(part)
		lines := wrapANSI(highlighted, inner)
		if len(lines) == 0 {
			lines = []string{""}
		}
		for li, ln := range lines {
			if li == 0 {
				out = append(out, prefix+ln+reset)
			} else {
				out = append(out, contPad+ln+reset)
			}
		}
	}
	if len(out) == 0 {
		out = []string{firstPrefix + reset}
	}
	return out
}

// bashToolCommandLines returns highlighted bash command rows for tool cards.
func bashToolCommandLines(command string, width int) []string {
	return wrapHighlightedBashMultiline(command, width, false)
}
