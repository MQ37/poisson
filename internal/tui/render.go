package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mq37/poisson/internal/tools"
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

// providerTag renders "[anthropic]" for a chosen backend, "" for the
// default — which backend ran is the difference between a free scrape and a
// billed API call, so it belongs right after the tool's name wherever that
// name is shown (a card title, a batch summary entry, an expanded batch
// line), not buried in a query preview that can get truncated away.
func providerTag(provider string) string {
	if provider == "" {
		return ""
	}
	return "[" + previewText(provider, 20) + "]"
}

// providerTagFromInput reads the generic "provider" field out of a tool
// call's own input JSON — used generically since batch is tool-agnostic, so
// this has to work for whichever tool (web_ask/web_search/fetch today) the
// model picked, not just one name.
func providerTagFromInput(input json.RawMessage) string {
	var in struct {
		Provider string `json:"provider"`
	}
	if len(input) == 0 || json.Unmarshal(input, &in) != nil {
		return ""
	}
	return providerTag(in.Provider)
}

// sandboxTagFromInput reads the generic "sandboxId" field out of a tool
// call's own input JSON — bash (and anything else routed through a sandbox)
// carries it — so a call running inside a sandbox container shows
// "Bash[my-sandbox]" instead of a bare "Bash", the same way providerTag
// distinguishes which backend served a call. Same generic lookup as
// providerTagFromInput so it also covers batch's nested per-call rendering.
func sandboxTagFromInput(input json.RawMessage) string {
	var in struct {
		SandboxID string `json:"sandboxId"`
	}
	if len(input) == 0 || json.Unmarshal(input, &in) != nil || in.SandboxID == "" {
		return ""
	}
	return "[" + previewText(in.SandboxID, 30) + "]"
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
	case "search", "grep", "glob":
		var in struct {
			Pattern string `json:"pattern"`
		}
		if json.Unmarshal(input, &in) == nil && in.Pattern != "" {
			return previewText(in.Pattern, 100)
		}
	case "batch":
		var in struct {
			Calls json.RawMessage `json:"calls"`
		}
		if json.Unmarshal(input, &in) == nil && len(in.Calls) > 0 {
			var calls []struct {
				Tool  string          `json:"tool"`
				Input json.RawMessage `json:"input"`
			}
			if json.Unmarshal(in.Calls, &calls) == nil && len(calls) > 0 {
				names := make([]string, 0, len(calls))
				for _, c := range calls {
					if c.Tool != "" {
						names = append(names, tools.CanonicalToolName(c.Tool)+providerTagFromInput(c.Input))
					}
				}
				if len(names) > 0 {
					return previewText(fmt.Sprintf("%d calls: %s", len(names), strings.Join(names, ", ")), 100)
				}
			}
			return fmt.Sprintf("%d calls", len(calls))
		}
	case "fetch":
		var in struct {
			URL      string `json:"url"`
			Provider string `json:"provider"`
		}
		if json.Unmarshal(input, &in) == nil && in.URL != "" {
			return previewText(in.URL, 100)
		}
	case "web_search", "web_ask":
		var in struct {
			Query    string `json:"query"`
			Provider string `json:"provider"`
		}
		if json.Unmarshal(input, &in) == nil && in.Query != "" {
			return previewText(in.Query, 100)
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

// diffToolPathAndSuffix returns the raw (untruncated) path plus the trailing
// summary suffix — " (N bytes)" for write, " (N edit(s))" for edit — for a
// write/edit tool card. Kept separate from toolInputPreview so the diff
// tool's header can truncate the path to the actual terminal width (and the
// user can expand to see the untruncated path — see toggleToolExpandBlock
// and layoutDiffTool) instead of toolInputPreview's fixed 80-byte cap, which
// silently ate long paths with no way to ever see them.
func diffToolPathAndSuffix(name string, input []byte) (path, suffix string, ok bool) {
	switch name {
	case "write":
		var in struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if json.Unmarshal(input, &in) == nil && in.Path != "" {
			return in.Path, fmt.Sprintf(" (%d bytes)", len(in.Content)), true
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
			return in.Path, fmt.Sprintf(" (%d %s)", n, unit), true
		}
	}
	return "", "", false
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
