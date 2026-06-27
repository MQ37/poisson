package tui

import (
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// StatusSnapshot is the data rendered in the 2-line status bar at the bottom
// of the split-screen TUI. It is updated from OutputStatus events.
type StatusSnapshot struct {
	SessionID      string
	Cwd            string
	Branch         string
	Model          string
	Effort         string
	ContextPct     float64
	ContextTokens  int
	ContextWindow  int
	OutputTokens   int
	CacheRead      int
	CacheWrite     int
	Cost           float64
	CallCount      int
	ToolCalls      int
	ToolErrors     int
	Thinking       bool
	SpinnerFrame   int
	WarnContext    bool
	Hint           string
	ShowTokens     bool
	ShowCost       bool
}

// RenderHeader returns a single Grok-style top strip: cwd left, tokens/model/time right.
func (s StatusSnapshot) RenderHeader(width int) string {
	if width < 20 {
		width = 80
	}
	left := s.renderHeaderLeft()
	right := s.renderHeaderRight()
	gap := width - visibleWidth(left) - visibleWidth(right)
	if gap < 1 {
		gap = 1
	}
	return truncateToWidth(left+strings.Repeat(" ", gap)+right, width)
}

func (s StatusSnapshot) renderHeaderLeft() string {
	cwd := shortenPath(s.Cwd, 36)
	if cwd == "" {
		cwd = "."
	}
	return dim + " " + fgBlue + cwd + reset
}

func (s StatusSnapshot) renderHeaderRight() string {
	var b strings.Builder
	if s.ShowTokens && s.ContextWindow > 0 {
		b.WriteString(fgCyan)
		b.WriteString(formatNum(s.ContextTokens))
		b.WriteString(reset)
		b.WriteString(dim + " / " + reset)
		b.WriteString(fgGray)
		b.WriteString(formatNum(s.ContextWindow))
		b.WriteString(reset)
		b.WriteString("  ")
	}
	if s.Thinking {
		b.WriteString(spinnerChar(s.SpinnerFrame))
		b.WriteString(" ")
	}
	if s.Model != "" {
		b.WriteString(fgMagenta)
		b.WriteString(shortenPath(s.Model, 24))
		b.WriteString(reset)
		b.WriteString("  ")
	}
	b.WriteString(dim)
	b.WriteString(time.Now().Format("3:04 PM"))
	b.WriteString(reset)
	b.WriteString(" ")
	return b.String()
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
	id := s.SessionID
	if len(id) > 6 {
		id = id[:6]
	}
	if s.Branch != "" {
		return fmt.Sprintf(" %s %s%s@%s%s", id, fgBlue, cwd, s.Branch, reset)
	}
	return fmt.Sprintf(" %s %s%s%s", id, fgBlue, cwd, reset)
}

func (s StatusSnapshot) renderRight(width int) string {
	spinner := " "
	if s.Thinking {
		spinner = spinnerChar(s.SpinnerFrame)
	}
	model := s.Model
	if s.Effort != "" {
		model = s.Effort + " · " + model
	}
	return fmt.Sprintf("%s %s%s%s", spinner, fgMagenta, model, reset)
}

func (s StatusSnapshot) renderBottom(width int) string {
	var b strings.Builder
	b.WriteString(" ")

	if s.ShowTokens {
		b.WriteString(fgCyan)
		b.WriteString("↑")
		b.WriteString(reset)
		b.WriteString(formatNum(s.ContextTokens))
		b.WriteString(" ")
		b.WriteString(fgGreen)
		b.WriteString("↓")
		b.WriteString(reset)
		b.WriteString(formatNum(s.OutputTokens))
		if s.CacheRead > 0 || s.CacheWrite > 0 {
			b.WriteString(" ")
			b.WriteString(fgGray)
			b.WriteString("R⌫")
			b.WriteString(reset)
			b.WriteString(formatNum(s.CacheRead))
			b.WriteString(" ")
			b.WriteString(fgGray)
			b.WriteString("W✎")
			b.WriteString(reset)
			b.WriteString(formatNum(s.CacheWrite))
		}
		b.WriteString("  ")
	}

	if s.ShowCost {
		b.WriteString(fgYellow)
		b.WriteString("$")
		b.WriteString(reset)
		b.WriteString(fmt.Sprintf("%.4f", s.Cost))
		b.WriteString("  ")
	}

	b.WriteString(fgGray)
	b.WriteString(fmt.Sprintf("%.1f%%", s.ContextPct))
	if s.WarnContext {
		b.WriteString(" ⚠")
	}
	b.WriteString(reset)

	if s.ToolCalls > 0 || s.ToolErrors > 0 {
		b.WriteString("  ")
		b.WriteString(dim)
		if s.ToolErrors > 0 {
			b.WriteString(fmt.Sprintf("%d tools (%d err)", s.ToolCalls, s.ToolErrors))
		} else {
			b.WriteString(fmt.Sprintf("%d tools", s.ToolCalls))
		}
		b.WriteString(reset)
	} else if s.CallCount > 0 {
		b.WriteString("  ")
		b.WriteString(dim)
		b.WriteString(fmt.Sprintf("%d calls", s.CallCount))
		b.WriteString(reset)
	}

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