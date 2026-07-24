package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// isDiffTool reports whether a tool always renders as a borderless colored
// diff of its input (what it's about to write/change) rather than a
// collapsible result preview. edit and write both act on files the model
// already has full knowledge of — the useful thing to show a human is what
// changed, not the terse "edited path (2 edits)" that goes back to the model.
func isDiffTool(name string) bool {
	return name == "edit" || name == "write"
}

// diffLine is one row of a computed edit/write diff. sign is '+' (added,
// green bg), '-' (removed, red bg), or ' ' (blank separator between edits).
// lineNo is the 1-based absolute line number in the target file (0 = none).
type diffLine struct {
	sign   byte
	text   string
	lineNo int
}

// toolDiffLines computes diff lines for an edit/write tool call from its
// input JSON. Returns nil if name isn't a diff tool or the input can't be parsed.
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
	lines := splitContentLines(in.Content)
	out := make([]diffLine, len(lines))
	for i, ln := range lines {
		out[i] = diffLine{sign: '+', text: expandTabs(ln), lineNo: i + 1}
	}
	return out
}

// editDiffLines re-parses the same input JSON already sent to the model, so
// it must recognize every shape internal/tools/edit.go's parseEditInput
// accepts. Line numbers are absolute positions in the target file: each
// oldText is located in the on-disk file (best-effort) and both the removed
// and added sides of that hunk number from that start line. If the file
// can't be read or oldText isn't found, falls back to 1-based hunk-local
// numbers so something still shows.
func editDiffLines(input []byte) []diffLine {
	type edit struct {
		OldText string `json:"oldText"`
		NewText string `json:"newText"`
	}
	var in struct {
		Path    string          `json:"path"`
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

	fileContent := readFileForDiff(in.Path)

	var out []diffLine
	for i, e := range edits {
		if i > 0 {
			out = append(out, diffLine{sign: ' ', text: "", lineNo: 0})
		}
		start := hunkStartLine(fileContent, e.OldText)
		oldLines := splitContentLines(e.OldText)
		for j, ln := range oldLines {
			out = append(out, diffLine{sign: '-', text: expandTabs(ln), lineNo: start + j})
		}
		newLines := splitContentLines(e.NewText)
		for j, ln := range newLines {
			out = append(out, diffLine{sign: '+', text: expandTabs(ln), lineNo: start + j})
		}
	}
	return out
}

// hunkStartLine returns the 1-based absolute line of oldText in fileContent.
// Falls back to 1 when the file is unknown or the match isn't found (the
// edit tool itself would have failed in that case for a live call; for a
// resumed/orphan card we still want some numbering).
func hunkStartLine(fileContent, oldText string) int {
	if fileContent == "" || oldText == "" {
		return 1
	}
	idx := strings.Index(fileContent, oldText)
	if idx < 0 {
		return 1
	}
	return 1 + strings.Count(fileContent[:idx], "\n")
}

// readFileForDiff best-effort loads the target file so edit diffs can show
// absolute line numbers. Returns "" on any failure (missing path, unreadable,
// etc.) — callers fall back to hunk-local numbering.
func readFileForDiff(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) {
		if cwd, err := os.Getwd(); err == nil {
			path = filepath.Join(cwd, path)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	// Cap at 8 MiB — bigger files aren't useful for a line-number lookup and
	// would stall the layout path if the model somehow pointed at one.
	if len(data) > 8<<20 {
		return ""
	}
	return string(data)
}

func splitContentLines(s string) []string {
	if s == "" {
		return []string{""}
	}
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// expandTabs turns each '\t' into 4 spaces. Terminals advance the cursor on a
// raw tab without painting the active background, so a tab mid-line leaves a
// hole in the green/red fill; spaces paint cleanly. Matches sanitizeControls.
func expandTabs(s string) string {
	if !strings.Contains(s, "\t") {
		return s
	}
	return strings.ReplaceAll(s, "\t", "    ")
}

// diffBG returns the background style for a diff sign.
func diffBG(sign byte) string {
	switch sign {
	case '+':
		return bgDiffAdd
	case '-':
		return bgDiffDel
	default:
		return ""
	}
}

// diffFG returns the foreground style for the sign column.
func diffFG(sign byte) string {
	switch sign {
	case '+':
		return fgGreen
	case '-':
		return fgRed
	default:
		return dim
	}
}

// keepBG rewrites every hard reset inside s so the surrounding background
// stays painted. highlightLine emits reset after every token; without this
// the green/red fill would end mid-line and the trailing pad would be bare.
func keepBG(s, bg string) string {
	if bg == "" || s == "" {
		return s
	}
	return strings.ReplaceAll(s, reset, reset+bg)
}

// renderDiffLines paints borderless diff rows: " NN │± code" with green/red
// background on the whole line, naive syntax highlighting on the code, and a
// right-padded fill so the bg spans the terminal width.
func renderDiffLines(lines []diffLine, width int, lang string) []string {
	if width < 12 {
		width = 12
	}
	// Line-number column width from the largest number present.
	maxNo := 0
	for _, dl := range lines {
		if dl.lineNo > maxNo {
			maxNo = dl.lineNo
		}
	}
	numW := len(itoa(maxNo))
	if numW < 2 {
		numW = 2
	}
	// " NN │± " prefix: numW + space-in-num + │ + sign + space-before-code.
	prefixW := numW + 4
	codeW := width - prefixW
	if codeW < 8 {
		codeW = 8
	}

	var out []string
	for _, dl := range lines {
		if dl.sign == ' ' && dl.text == "" {
			// Blank separator between edit hunks.
			out = append(out, strings.Repeat(" ", width))
			continue
		}
		bg := diffBG(dl.sign)
		fg := diffFG(dl.sign)
		num := strings.Repeat(" ", numW)
		if dl.lineNo > 0 {
			n := itoa(dl.lineNo)
			num = strings.Repeat(" ", numW-len(n)) + n
		}
		signCh := string(dl.sign)
		if dl.sign != '+' && dl.sign != '-' {
			signCh = " "
		}

		// Tabs already expanded in toolDiffLines; belt-and-suspenders here so
		// any direct caller of renderDiffLines still paints a solid bg.
		text := expandTabs(dl.text)

		// Highlight the code (per logical line), then wrap. keepBG so each
		// token's reset doesn't kill the row's green/red fill.
		hi := keepBG(highlightLine(lang, text), bg)
		wrapped := wrapANSI(hi, codeW)
		if len(wrapped) == 0 {
			wrapped = []string{""}
		}
		for i, chunk := range wrapped {
			var b strings.Builder
			// Open the row under bg and never leave it until the final reset.
			// Every style change is reset+bg (via keepBG on the code) so the
			// green/red fill is continuous across number, bar, sign, code, pad.
			b.WriteString(bg)
			if i == 0 {
				b.WriteString(dim)
				b.WriteString(num)
				b.WriteString(reset)
				b.WriteString(bg)
				b.WriteString(dim)
				b.WriteString(" │")
				b.WriteString(reset)
				b.WriteString(bg)
				b.WriteString(fg)
				b.WriteString(signCh)
				b.WriteString(reset)
				b.WriteString(bg)
				b.WriteString(" ")
			} else {
				// Continuation: indent under the code column (still on bg).
				b.WriteString(strings.Repeat(" ", prefixW))
			}
			b.WriteString(chunk)
			// Pad under bg so the fill spans the full terminal width.
			used := prefixW + visibleWidth(chunk)
			if used < width {
				// chunk may end mid-token-style; force bg for the pad.
				b.WriteString(reset)
				b.WriteString(bg)
				b.WriteString(strings.Repeat(" ", width-used))
			}
			b.WriteString(reset)
			out = append(out, b.String())
		}
	}
	return out
}

// toolExpandLineCount returns how many wrapped rows a tool's expandable
// content has at the given width. Diff tools don't paginate (always full);
// other tools use the plain result preview, clamped to toolResultExpandedMax.
func toolExpandLineCount(b *Block, width int) int {
	if isDiffTool(b.meta.ToolName) && b.meta.ToolError == "" {
		// Full diff is always shown; no internal scroll window.
		lang := toolLangFromInput(b.meta.ToolName, b.meta.ToolInput)
		return len(renderDiffLines(toolDiffLines(b.meta.ToolName, b.meta.ToolInput), width, lang))
	}
	inner := width
	if inner < 1 {
		inner = 1
	}
	lines := wrapLine(toolResultFullText(b), inner)
	if len(lines) > toolResultExpandedMax {
		lines = lines[:toolResultExpandedMax]
	}
	return len(lines)
}
