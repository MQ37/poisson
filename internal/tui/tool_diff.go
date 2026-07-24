package tui

import (
	"encoding/json"
	"fmt"
	"strings"
)

// isDiffTool reports whether a tool's expandable content is a colored diff
// of its input (what it's about to write/change) rather than a preview of
// its result. edit and write both act on files the model already has full
// knowledge of — the useful thing to show a human is what changed, not the
// terse "edited path (2 edits)" that goes back to the model as ToolResult.
func isDiffTool(name string) bool {
	return name == "edit" || name == "write"
}

// diffLine is one row of a computed edit/write diff. sign is '+' (added,
// green), '-' (removed, red), or ' ' (blank separator between edits, no
// color).
type diffLine struct {
	sign byte
	text string
}

// toolDiffLines computes diff lines for an edit/write tool call from its
// input JSON (the same input already sent to the model — no extra tokens
// spent reconstructing this for display). Returns nil if name isn't a diff
// tool or the input can't be parsed.
func toolDiffLines(name string, input []byte) []diffLine {
	switch name {
	case "write":
		return writeDiffLines(input)
	case "edit":
		return editDiffLines(input)
	}
	return nil
}

func writeDiffLines(input []byte) []diffLine {
	var in struct {
		Content string `json:"content"`
	}
	if json.Unmarshal(input, &in) != nil {
		return nil
	}
	lines := strings.Split(in.Content, "\n")
	out := make([]diffLine, len(lines))
	for i, ln := range lines {
		out[i] = diffLine{sign: '+', text: ln}
	}
	return out
}

// editDiffLines re-parses the same input JSON already sent to the model, so
// it must recognize every shape internal/tools/edit.go's parseEditInput
// accepts: the documented edits: [{oldText, newText}, ...] array, the flat
// top-level {oldText, newText} shorthand for a single edit, and edits sent
// as a JSON-encoded string (some models double-encode the array). Duplicated
// here rather than imported from internal/tools to keep this package's own
// display-only reparse self-contained, matching writeDiffLines just below.
func editDiffLines(input []byte) []diffLine {
	type edit struct {
		OldText string `json:"oldText"`
		NewText string `json:"newText"`
	}
	var in struct {
		Edits   json.RawMessage `json:"edits"`
		OldText string          `json:"oldText"`
		NewText string          `json:"newText"`
	}
	if json.Unmarshal(input, &in) != nil {
		return nil
	}
	var edits []edit
	switch {
	case len(in.Edits) > 0 && string(in.Edits) != "null":
		if json.Unmarshal(in.Edits, &edits) != nil {
			var asString string
			if json.Unmarshal(in.Edits, &asString) == nil {
				json.Unmarshal([]byte(asString), &edits)
			}
		}
	case in.OldText != "":
		edits = []edit{{OldText: in.OldText, NewText: in.NewText}}
	}
	if len(edits) == 0 {
		return nil
	}
	var out []diffLine
	for i, e := range edits {
		if i > 0 {
			out = append(out, diffLine{sign: ' ', text: ""})
		}
		for _, ln := range strings.Split(e.OldText, "\n") {
			out = append(out, diffLine{sign: '-', text: ln})
		}
		for _, ln := range strings.Split(e.NewText, "\n") {
			out = append(out, diffLine{sign: '+', text: ln})
		}
	}
	return out
}

// diffLinePrefix returns the sign column and its color for a diff line.
func diffLinePrefix(sign byte) (prefix, style string) {
	switch sign {
	case '+':
		return "+ ", fgGreen
	case '-':
		return "- ", fgRed
	default:
		return "  ", ""
	}
}

// wrapDiffLines wraps diff lines to width visible columns, returning
// ANSI-colored strings ready to render \u2014 one per screen row. Every wrapped
// continuation row repeats the sign's color: dirty-row repaints address rows
// independently, so color can't carry over from a row outside the current
// repaint batch.
func wrapDiffLines(lines []diffLine, width int) []string {
	if width < 1 {
		width = 1
	}
	var out []string
	for _, dl := range lines {
		prefix, style := diffLinePrefix(dl.sign)
		inner := width - len(prefix)
		if inner < 1 {
			inner = 1
		}
		for _, w := range wrapLine(dl.text, inner) {
			out = append(out, style+prefix+w+reset)
		}
	}
	return out
}

// toolDiffNeedsExpand reports whether a diff tool's content is long enough
// to warrant the expand affordance. Mirrors toolResultNeedsExpand's
// byte/line heuristics but works from line count directly since diff lines
// are already split \u2014 callers (e.g. the expand-toggle keybinding) don't
// always have a real render width on hand.
func toolDiffNeedsExpand(b *Block) bool {
	lines := toolDiffLines(b.meta.ToolName, b.meta.ToolInput)
	if len(lines) > toolResultCollapsedLines {
		return true
	}
	total := 0
	for _, l := range lines {
		total += len(l.text)
	}
	return total > toolResultCollapsedBytes
}

// toolExpandLineCount returns how many wrapped rows a tool's expandable
// content has at the given width \u2014 the diff for edit/write, or the plain
// result preview for everything else \u2014 clamped the same way the actual
// rendering paths clamp (toolResultExpandedMax). Used to compute scroll
// bounds and whether pagination applies.
func toolExpandLineCount(b *Block, width int) int {
	inner := width - 4
	if inner < 1 {
		inner = 1
	}
	var lines []string
	if isDiffTool(b.meta.ToolName) && b.meta.ToolError == "" {
		lines = wrapDiffLines(toolDiffLines(b.meta.ToolName, b.meta.ToolInput), inner)
	} else {
		lines = wrapLine(toolResultFullText(b), inner)
	}
	if len(lines) > toolResultExpandedMax {
		lines = lines[:toolResultExpandedMax]
	}
	return len(lines)
}

// diffCardCollapsedLines returns the pre-styled, boxed-ready diff preview
// rows shown before the footer when a diff tool is done but not expanded.
func diffCardCollapsedLines(b *Block, width int) []string {
	inner := width - 4
	if inner < 1 {
		inner = 1
	}
	all := wrapDiffLines(toolDiffLines(b.meta.ToolName, b.meta.ToolInput), inner)
	if len(all) == 0 {
		return []string{""}
	}
	lines := all
	if len(lines) > toolResultCollapsedLines {
		lines = lines[:toolResultCollapsedLines]
	}
	lines = append([]string(nil), lines...) // don't mutate all's backing array below
	last := len(lines) - 1
	suffix := ""
	if b.meta.DurationMs > 0 {
		suffix = fmt.Sprintf(" · %.1fs", float64(b.meta.DurationMs)/1000)
	}
	suffix += toolCardSpeedSuffix(b)
	hint := ""
	if toolDiffNeedsExpand(b) {
		hint = " · click/Ctrl+E"
	}
	avail := inner - visibleWidth(suffix+hint)
	if avail < 4 {
		avail = inner
		suffix = ""
		hint = ""
	}
	lines[last] = truncateToWidth(lines[last], avail) + reset + dim + suffix + hint + reset
	return lines
}

// diffCardExpandedLines returns the pre-styled, paginated diff rows shown
// when a diff tool card is expanded, honoring b.meta.ToolScroll.
func diffCardExpandedLines(b *Block, width int) []string {
	inner := width - 4
	if inner < 1 {
		inner = 1
	}
	lines := wrapDiffLines(toolDiffLines(b.meta.ToolName, b.meta.ToolInput), inner)
	total := len(lines)
	truncated := total > toolResultExpandedMax
	if truncated {
		lines = lines[:toolResultExpandedMax]
	}
	start := b.meta.ToolScroll
	if start < 0 {
		start = 0
	}
	if start > len(lines) {
		start = len(lines)
	}
	end := start + toolResultExpandedView
	if end > len(lines) {
		end = len(lines)
	}
	if start >= end {
		return nil
	}
	out := lines[start:end]
	if truncated && end >= len(lines) {
		remaining := total - toolResultExpandedMax
		out = append(out, fmt.Sprintf("%s… %d more lines (↑↓ scroll)%s", dim, remaining, reset))
	}
	return out
}
