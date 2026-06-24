package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"poisson/internal/agent"
)

// renderEvent renders a single OutputEvent to the TUI's writer based on its
// type. The testable core is renderEventString.
func (t *TUI) renderEvent(ev agent.OutputEvent) {
	if ev.Type == agent.OutputStatus {
		t.writeString(renderStatusBarString(ev, t.sessionID))
		return
	}
	t.writeString(renderEventString(ev))
}

// renderEventString formats an OutputEvent into a string without writing to a
// terminal. This is the testable core of the rendering logic.
func renderEventString(ev agent.OutputEvent) string {
	switch ev.Type {
	case agent.OutputText:
		return terminalText(ev.Text)

	case agent.OutputToolStart:
		var b strings.Builder
		b.WriteString("\r\n  [")
		b.WriteString(ev.ToolName)
		b.WriteString("] ")
		b.WriteString(toolInputPreview(ev.ToolName, ev.ToolInput))
		b.WriteString("\r\n  ⠋ working...\r\n")
		return b.String()

	case agent.OutputToolResult:
		var b strings.Builder
		if ev.ToolError != "" {
			b.WriteString("  ✗ ")
			b.WriteString(previewText(ev.ToolError, 200))
			b.WriteString("\r\n")
		} else {
			b.WriteString("  ✓ ")
			b.WriteString(toolResultPreview(ev.ToolName, ev.ToolResultContent))
			b.WriteString("\r\n")
		}
		return b.String()

	case agent.OutputApproval:
		return fmt.Sprintf("\r\n  ⚠ approval needed for: %s\r\n", ev.ToolName)

	case agent.OutputError:
		return fmt.Sprintf("error: %s\r\n", ev.Text)

	case agent.OutputCompacting:
		return "\r\n  compacting context...\r\n"

	default:
		return ""
	}
}

func toolInputPreview(toolName string, input []byte) string {
	if len(input) == 0 {
		return "..."
	}
	switch toolName {
	case "bash":
		var in struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(input, &in) == nil && in.Command != "" {
			return "$ " + previewText(in.Command, 100)
		}
	case "write":
		var in struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if json.Unmarshal(input, &in) == nil && in.Path != "" {
			return fmt.Sprintf("%s (%d bytes)", previewText(in.Path, 80), len(in.Content))
		}
	case "read", "edit":
		var in struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(input, &in) == nil && in.Path != "" {
			return previewText(in.Path, 100)
		}
	case "search", "glob":
		var in struct {
			Pattern string `json:"pattern"`
		}
		if json.Unmarshal(input, &in) == nil && in.Pattern != "" {
			return previewText(in.Pattern, 100)
		}
	case "fetch":
		var in struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(input, &in) == nil && in.URL != "" {
			return previewText(in.URL, 100)
		}
	}
	return previewText(strings.TrimSpace(string(input)), 80)
}

func toolResultPreview(toolName, content string) string {
	if toolName == "bash" {
		var out struct {
			Stdout   string `json:"stdout"`
			Stderr   string `json:"stderr"`
			ExitCode int    `json:"exitCode"`
		}
		if json.Unmarshal([]byte(content), &out) == nil {
			if out.Stderr != "" {
				return previewText(out.Stderr, 200)
			}
			if out.Stdout != "" {
				return previewText(out.Stdout, 200)
			}
			return fmt.Sprintf("exit %d", out.ExitCode)
		}
	}
	return previewText(content, 200)
}

func terminalText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}

func previewText(s string, maxBytes int) string {
	truncated := false
	if len(s) > maxBytes {
		cut := maxBytes
		for cut > 0 && (s[cut]&0xc0) == 0x80 {
			cut--
		}
		s = s[:cut]
		truncated = true
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteByte(' ')
		case r < 32 || r == 127:
			b.WriteByte('?')
		default:
			b.WriteRune(r)
		}
	}
	if truncated {
		b.WriteString("...")
	}
	return b.String()
}

// renderStatusBar formats and writes the status bar for a status event.
func (t *TUI) renderStatusBar(ev agent.OutputEvent) {
	t.writeString(renderStatusBarString(ev, t.sessionID))
}

// renderStatusBarString formats the status bar line:
//
//	[session] ctx: 42.3% (12,847/30,400) | $0.0124 | provider/model
//
// A ⚠ warning is appended when context usage exceeds 75%.
func renderStatusBarString(ev agent.OutputEvent, sessionID string) string {
	var b strings.Builder
	b.WriteString("\r\n[")
	b.WriteString(shortID(sessionID))
	b.WriteString("] ctx: ")
	b.WriteString(fmt.Sprintf("%.1f%%", ev.ContextPct))
	b.WriteString(" (")
	b.WriteString(formatNum(ev.ContextTokens))
	b.WriteString("/")
	b.WriteString(formatNum(ev.ContextWindow))
	b.WriteString(") | $")
	b.WriteString(fmt.Sprintf("%.4f", ev.Cost))
	b.WriteString(" | ")
	b.WriteString(ev.Model)
	if ev.ContextPct > 75.0 {
		b.WriteString(" ⚠")
	}
	b.WriteString("\r\n")
	return b.String()
}

// shortID returns the first 6 characters of an ID, or the whole string if
// shorter.
func shortID(id string) string {
	if len(id) > 6 {
		return id[:6]
	}
	return id
}

// formatNum formats an integer with thousands separators (commas).
func formatNum(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
		if len(s) > pre {
			b.WriteString(",")
		}
	}
	for i := pre; i < len(s); i += 3 {
		b.WriteString(s[i : i+3])
		if i+3 < len(s) {
			b.WriteString(",")
		}
	}
	return b.String()
}

// clearLine erases the current line and moves the cursor to column 1.
func (t *TUI) clearLine() {
	t.writeString("\x1b[2K\r")
}

// moveCursor moves the cursor by n columns. dir is 'C' (right) or 'D' (left).
func (t *TUI) moveCursor(dir byte, n int) {
	if n <= 0 {
		return
	}
	t.writeString(fmt.Sprintf("\x1b[%d%c", n, dir))
}

// clearScreen clears the entire screen and homes the cursor.
func (t *TUI) clearScreen() {
	t.writeString("\x1b[2J\x1b[H")
}
