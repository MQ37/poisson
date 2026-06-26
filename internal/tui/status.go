package tui

import (
	"fmt"
	"os/exec"
	"strings"
)

// StatusSnapshot is the data rendered in the 2-line status bar at the bottom
// of the split-screen TUI. It is updated from OutputStatus events.
type StatusSnapshot struct {
	SessionID     string
	Cwd           string
	Branch        string
	Model         string
	Effort        string
	ContextPct    float64
	ContextTokens int
	ContextWindow int
	Cost          float64
	CallCount     int
	Thinking      bool // spinner state while a prompt is streaming
	Hint          string
}

// Render returns the two status lines joined with a "\n" separator. The
// returned string already includes the per-side ANSI; it does NOT include
// the trailing newline that the scrollback uses for layout.
func (s StatusSnapshot) Render(width int) string {
	if width < 20 {
		width = 80
	}
	left := s.renderLeft(width)
	right := s.renderRight(width)
	gap := width - visibleWidth(left) - visibleWidth(right)
	if gap < 1 {
		gap = 1
	}
	top := truncateToWidth(dim+left+strings.Repeat(" ", gap)+right+reset, width)

	bottom := s.renderBottom(width)
	return top + "\r\n" + bottom
}

func (s StatusSnapshot) renderLeft(width int) string {
	cwd := shortenPath(s.Cwd, 30)
	if s.Branch != "" {
		return fmt.Sprintf(" %s %s%s@%s%s %s", s.SessionID[:min6(len(s.SessionID))],
			fgBlue, cwd, s.Branch, reset, cwdLabel())
	}
	return fmt.Sprintf(" %s %s%s%s", s.SessionID[:min6(len(s.SessionID))], fgBlue, cwd, reset)
}

func (s StatusSnapshot) renderRight(width int) string {
	spinner := " "
	if s.Thinking {
		spinner = "⠋"
	}
	return fmt.Sprintf("%s %s%s%s", spinner, fgMagenta, s.Model, reset)
}

func (s StatusSnapshot) renderBottom(width int) string {
	// Bottom row: token usage + cost + context %.
	var b strings.Builder
	b.WriteString(" ")
	b.WriteString(fgCyan)
	b.WriteString("↑in ")
	b.WriteString(reset)
	b.WriteString(formatNum(s.ContextTokens))
	b.WriteString("  ")
	b.WriteString(fgYellow)
	b.WriteString("$")
	b.WriteString(reset)
	costStr := fmt.Sprintf("%.4f", s.Cost)
	b.WriteString(costStr)
	b.WriteString("  ")
	b.WriteString(fgGray)
	pctStr := fmt.Sprintf("%.1f%%", s.ContextPct)
	b.WriteString(pctStr)
	b.WriteString(reset)
	if s.Hint != "" {
		b.WriteString("  ")
		b.WriteString(dim)
		b.WriteString(s.Hint)
		b.WriteString(reset)
	}
	return truncateToWidth(b.String(), width)
}

func min6(n int) int {
	if n < 6 {
		return n
	}
	return 6
}

// shortenPath collapses $HOME to ~ and truncates the middle if longer than n.
func shortenPath(p string, n int) string {
	if home := homeDir(); home != "" && strings.HasPrefix(p, home) {
		p = "~" + p[len(home):]
	}
	if len(p) <= n {
		return p
	}
	if n < 4 {
		return p[:n]
	}
	head := (n - 1) / 2
	tail := n - 1 - head
	return p[:head] + "…" + p[len(p)-tail:]
}

func homeDir() string {
	// Avoid os.UserHomeDir() to keep this pure for tests.
	for _, env := range []string{"HOME", "USERPROFILE"} {
		if v := getenv(env); v != "" {
			return v
		}
	}
	return ""
}

func cwdLabel() string { return fgBlue }

// gitBranch returns the current git branch name or "" if not in a repo.
func gitBranch(cwd string) string {
	if cwd == "" {
		return ""
	}
	cmd := exec.Command("git", "-C", cwd, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	b := strings.TrimSpace(string(out))
	if b == "" || b == "HEAD" {
		return ""
	}
	return b
}
