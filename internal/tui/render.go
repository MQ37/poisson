package tui

import (
	"encoding/json"
	"fmt"
	"strings"
)

// bashInputPreview summarizes a bash command for the tool card body.
// Multi-line scripts show the first line plus a line count instead of
// dumping the full heredoc into scrollback.
func bashInputPreview(command string) string {
	normalized := strings.ReplaceAll(command, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	nonEmpty := 0
	first := ""
	for _, ln := range lines {
		s := strings.TrimSpace(ln)
		if s == "" {
			continue
		}
		nonEmpty++
		if first == "" {
			first = s
		}
	}
	if first == "" {
		return "$ " + previewText(command, 80)
	}
	out := "$ " + previewText(first, 80)
	if nonEmpty > 1 {
		out += dim + " (+" + itoa(nonEmpty-1) + " lines)" + reset
	} else if len(command) > 80 {
		out = "$ " + previewText(command, 80)
	}
	return out
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
			return bashInputPreview(in.Command)
		}
	case "write":
		var in struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if json.Unmarshal(input, &in) == nil && in.Path != "" {
			return fmt.Sprintf("%s (%d bytes)", previewText(in.Path, 80), len(in.Content))
		}
	case "read", "@file":
		var in struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(input, &in) == nil && in.Path != "" {
			return previewText(in.Path, 100)
		}
	case "edit":
		var in struct {
			Path    string          `json:"path"`
			Edits   json.RawMessage `json:"edits"`
			OldText string          `json:"oldText"`
		}
		if json.Unmarshal(input, &in) == nil && in.Path != "" {
			n := editCountFromInput(in.Edits, in.OldText)
			unit := "edit"
			if n != 1 {
				unit = "edits"
			}
			return fmt.Sprintf("%s (%d %s)", previewText(in.Path, 80), n, unit)
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
	case "@image":
		var in struct {
			Name string `json:"name"`
			Size int    `json:"size"`
		}
		if json.Unmarshal(input, &in) == nil && in.Name != "" {
			if in.Size > 0 {
				return fmt.Sprintf("%s · %s", previewText(in.Name, 80), humanBytes(in.Size))
			}
			return previewText(in.Name, 80)
		}
	}
	return previewText(strings.TrimSpace(string(input)), 80)
}

// editCountFromInput counts edits across every shape parseEditInput
// (internal/tools/edit.go) accepts: the edits: [...] array, that array
// double-encoded as a JSON string, or the flat {oldText, newText} shorthand
// for a single edit. Mirrors editDiffLines' shape handling in tool_diff.go —
// otherwise the flat shorthand shows as "(0 edits)" in the card header even
// though the diff body below renders a real change.
func editCountFromInput(edits json.RawMessage, oldText string) int {
	if len(edits) > 0 && string(edits) != "null" {
		var arr []any
		if json.Unmarshal(edits, &arr) == nil {
			return len(arr)
		}
		var asString string
		if json.Unmarshal(edits, &asString) == nil {
			if json.Unmarshal([]byte(asString), &arr) == nil {
				return len(arr)
			}
		}
		return 0
	}
	if oldText != "" {
		return 1
	}
	return 0
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
