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
	Title         string
	Cwd           string
	Branch        string
	Model         string
	Effort        string
	ContextPct    float64
	ContextTokens int
	ContextWindow int
	OutputTokens  int
	CacheRead     int
	CacheWrite    int
	Cost          float64
	CallCount     int
	ToolCalls     int
	ToolErrors    int
	Turns         int
	Thinking      bool
	SpinnerFrame  int
	WarnContext   bool
	Hint          string
	ShowTokens    bool
	ShowCost      bool
}

// RenderHeader returns a single Grok-style top strip: cwd left, tokens/model right.
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
	if s.Title != "" {
		title := shortenPath(s.Title, 28)
		return dim + " " + fgBlue + title + dim + " · " + reset + fgBlue + cwd + reset
	}
	return dim + " " + fgBlue + cwd + reset
}

func (s StatusSnapshot) renderHeaderRight() string {
	var b strings.Builder
	// Show the context N / window whenever it's configured on, and always while
	// the agent is working so the running status bar carries it.
	if (s.ShowTokens || s.Thinking) && s.ContextWindow > 0 {
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
		if s.Turns > 0 {
			b.WriteString(dim + "turn " + reset + fgGreen + fmt.Sprintf("%d", s.Turns) + reset + "  ")
		}
		b.WriteString(spinnerChar(s.SpinnerFrame))
		b.WriteString(" ")
	}
	if s.Model != "" {
		label := s.Model
		if s.Effort != "" {
			label = s.Effort + " · " + label
		}
		b.WriteString(fgMagenta)
		b.WriteString(label)
		b.WriteString(reset)
		b.WriteString("  ")
	}
	if s.ShowCost && s.Cost > 0 {
		b.WriteString(fgYellow)
		b.WriteString(fmt.Sprintf("$%.4f", s.Cost))
		b.WriteString(reset)
		b.WriteString("  ")
	}
	if s.WarnContext {
		b.WriteString(fgYellow)
		b.WriteString(fmt.Sprintf("%.0f%%⚠", s.ContextPct))
		b.WriteString(reset)
		b.WriteString("  ")
	}
	if s.ToolCalls > 0 || s.ToolErrors > 0 {
		b.WriteString(dim)
		if s.ToolErrors > 0 {
			b.WriteString(fmt.Sprintf("%dT/%de", s.ToolCalls, s.ToolErrors))
		} else {
			b.WriteString(fmt.Sprintf("%dT", s.ToolCalls))
		}
		b.WriteString(reset)
		b.WriteString("  ")
	}
	return b.String()
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
