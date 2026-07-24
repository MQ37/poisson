package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode"

	"github.com/mq37/poisson/internal/guard"
)

// EditTool edits a file using exact text replacement.
type EditTool struct {
	cwd        string
	sandbox    bool
	approvalFn ApprovalFn
}

func NewEditTool(cwd string, sandbox bool, approvalFn ApprovalFn) *EditTool {
	return &EditTool{cwd: cwd, sandbox: sandbox, approvalFn: approvalFn}
}

func (t *EditTool) Name() string { return "edit" }

func (t *EditTool) Description() string {
	return "Edit a file using exact text replacement. Every edits[].oldText must match a unique, non-overlapping region unless replaceAll is true. For a single edit you can also pass oldText/newText directly at the top level instead of wrapping it in edits: [{...}]. Set replaceAll to replace every occurrence (handy for renaming). Prefer this over bash sed/awk for in-place edits."
}

func (t *EditTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": { "type": "string" },
    "edits": {
      "type": "array",
      "description": "Use this for 2+ edits in one call. For a single edit, oldText/newText below are simpler.",
      "items": {
        "type": "object",
        "properties": {
          "oldText": { "type": "string" },
          "newText": { "type": "string" },
          "replaceAll": { "type": "boolean", "description": "Replace every occurrence of oldText (default false)." }
        },
        "required": ["oldText", "newText"]
      }
    },
    "oldText": { "type": "string", "description": "Shorthand for a single edit — use instead of edits: [{...}] when there's only one." },
    "newText": { "type": "string" },
    "replaceAll": { "type": "boolean", "description": "Shorthand replaceAll for a single top-level oldText/newText edit. Also the default for edits[] items that omit their own replaceAll." }
  },
  "required": ["path"]
}`)
}

type editItem struct {
	OldText    string `json:"oldText"`
	NewText    string `json:"newText"`
	ReplaceAll bool   `json:"replaceAll"`
	// replaceAllSet distinguishes "omitted" from "explicit false" so a
	// top-level replaceAll can fill in for array items that didn't set one.
	replaceAllSet bool
}

func (e *editItem) UnmarshalJSON(data []byte) error {
	var raw struct {
		OldText    string          `json:"oldText"`
		NewText    string          `json:"newText"`
		ReplaceAll json.RawMessage `json:"replaceAll"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	e.OldText = raw.OldText
	e.NewText = raw.NewText
	if len(raw.ReplaceAll) > 0 && string(raw.ReplaceAll) != "null" {
		var b bool
		if err := json.Unmarshal(raw.ReplaceAll, &b); err != nil {
			return fmt.Errorf("replaceAll: %w", err)
		}
		e.ReplaceAll = b
		e.replaceAllSet = true
	}
	return nil
}

// rawEditInput mirrors the wire shape loosely: Edits is left raw so
// parseEditInput can recognize shapes beyond a strict array-of-objects —
// models calling this tool reliably reach for a flat top-level
// oldText/newText when there's only one edit, and occasionally double-encode
// the array as a JSON string. Both are handled below instead of just erroring.
type rawEditInput struct {
	Path       string          `json:"path"`
	Edits      json.RawMessage `json:"edits"`
	OldText    string          `json:"oldText"`
	NewText    string          `json:"newText"`
	ReplaceAll json.RawMessage `json:"replaceAll"`
}

// parseEditInput accepts three shapes for the edit list: the documented
// edits: [{oldText, newText}, ...] array; a flat top-level oldText/newText
// pair as shorthand for a single edit (no array wrapper needed); and, best
// effort, an edits value that's itself a JSON-encoded string containing that
// array (some models double-encode it). Confirmed against real tool-call
// failures logged against this tool: a flat single-edit call used to
// unmarshal with an empty Edits slice and fail with the unhelpful "no edits
// provided", and a string-encoded edits value failed with a raw Go
// json.Unmarshal type-mismatch error that gave the model nothing to correct.
func parseEditInput(input json.RawMessage) (path string, edits []editItem, err error) {
	var raw rawEditInput
	if e := json.Unmarshal(input, &raw); e != nil {
		return "", nil, fmt.Errorf("invalid input: %w", e)
	}
	path = raw.Path
	topReplaceAll, topReplaceAllSet := false, false
	if len(raw.ReplaceAll) > 0 && string(raw.ReplaceAll) != "null" {
		if e := json.Unmarshal(raw.ReplaceAll, &topReplaceAll); e != nil {
			return "", nil, fmt.Errorf("replaceAll: %w", e)
		}
		topReplaceAllSet = true
	}
	applyTopDefault := func(items []editItem) []editItem {
		if !topReplaceAllSet {
			return items
		}
		for i := range items {
			if !items[i].replaceAllSet {
				items[i].ReplaceAll = topReplaceAll
				items[i].replaceAllSet = true
			}
		}
		return items
	}
	if len(raw.Edits) == 0 || string(raw.Edits) == "null" {
		if raw.OldText != "" {
			return path, []editItem{{
				OldText:       raw.OldText,
				NewText:       raw.NewText,
				ReplaceAll:    topReplaceAll,
				replaceAllSet: topReplaceAllSet,
			}}, nil
		}
		return path, nil, nil
	}
	var items []editItem
	if e := json.Unmarshal(raw.Edits, &items); e == nil {
		return path, applyTopDefault(items), nil
	}
	var asString string
	if e := json.Unmarshal(raw.Edits, &asString); e == nil {
		if e2 := json.Unmarshal([]byte(asString), &items); e2 == nil {
			return path, applyTopDefault(items), nil
		}
	}
	return "", nil, fmt.Errorf("edits must be a JSON array of {oldText, newText} objects (e.g. edits: [{\"oldText\": \"...\", \"newText\": \"...\"}]), or oldText/newText directly for a single edit — not a JSON-encoded string")
}

// stripWS drops every unicode space so "func Foo(){" and "func Foo() {"
// compare equal. strings.Fields alone is not enough: removing a space next
// to punctuation merges tokens (Foo(){ vs Foo() + {) and misses the mismatch.
func stripWS(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}

func clipHintLine(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 80 {
		return s[:80] + "…"
	}
	return s
}

// levenshteinDistance is a small DP edit-distance for near-miss line hints.
// Inputs are expected short (single source lines); long strings are rejected
// by the caller before this runs.
func levenshteinDistance(a, b string) int {
	if a == b {
		return 0
	}
	ra, rb := []rune(a), []rune(b)
	na, nb := len(ra), len(rb)
	if na == 0 {
		return nb
	}
	if nb == 0 {
		return na
	}
	// Two-row rolling DP.
	prev := make([]int, nb+1)
	cur := make([]int, nb+1)
	for j := 0; j <= nb; j++ {
		prev[j] = j
	}
	for i := 1; i <= na; i++ {
		cur[0] = i
		for j := 1; j <= nb; j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := cur[j-1] + 1
			sub := prev[j-1] + cost
			m := del
			if ins < m {
				m = ins
			}
			if sub < m {
				m = sub
			}
			cur[j] = m
		}
		prev, cur = cur, prev
	}
	return prev[nb]
}

// closestMatchHint helps recover from a failed oldText match by naming the
// most likely cause instead of leaving the model to reread the whole file
// blind. Checked in order:
//  1. oldText exists but differs only in whitespace (including space deleted
//     next to punctuation — "Foo(){" vs "Foo() {")
//  2. longest old line appears verbatim as a substring of some file line
//  3. fuzzy near-miss: best Levenshtein ratio against the longest old line
//  4. keyword: longest token from first old line appears in some file line
//  5. generic re-read nudge
func closestMatchHint(original, oldText string) string {
	origLines := strings.Split(original, "\n")
	oldLines := strings.Split(oldText, "\n")

	// 1. Whitespace-only: strip all WS per line over a window of len(oldLines).
	stripOld := make([]string, len(oldLines))
	for i, l := range oldLines {
		stripOld[i] = stripWS(l)
	}
	// Ignore pure-blank old lines for the window compare only if entire old
	// is blank — otherwise require same window length including blanks so
	// multi-line blocks stay aligned.
	for start := 0; start+len(oldLines) <= len(origLines); start++ {
		match := true
		for i, so := range stripOld {
			if stripWS(origLines[start+i]) != so {
				match = false
				break
			}
		}
		if match {
			return fmt.Sprintf(" (whitespace-only mismatch at line %d — copy the exact text with the read tool instead of retyping it)", start+1)
		}
	}

	// Pick the most distinctive non-empty old line for substring / fuzzy.
	longest := ""
	for _, l := range oldLines {
		if trimmed := strings.TrimSpace(l); len(trimmed) > len(longest) {
			longest = trimmed
		}
	}

	// 2. Verbatim substring of a file line.
	if longest != "" {
		for i, l := range origLines {
			if strings.Contains(l, longest) {
				return fmt.Sprintf(" (closest line found is line %d: %s — re-read the file, it may have changed since your last read)", i+1, clipHintLine(l))
			}
		}
	}

	// 3. Fuzzy near-miss on the longest old line (catches baz vs bar, off-by-one
	// identifier typos). Cap line length so a pasted whole-file oldText can't
	// turn this into an O(n²·L²) scan.
	if longest != "" && len(longest) <= 200 {
		bestIdx, bestDist := -1, 1<<30
		longN := len([]rune(longest))
		// Accept if distance is small relative to length, or absolute ≤3 for
		// short identifiers ("baz"/"bar" → dist 1).
		maxDist := 3
		if longN/4 > maxDist {
			maxDist = longN / 4
		}
		for i, l := range origLines {
			cand := strings.TrimSpace(l)
			if cand == "" || len(cand) > 240 {
				continue
			}
			// Cheap reject: length gap already bigger than maxDist.
			candN := len([]rune(cand))
			gap := candN - longN
			if gap < 0 {
				gap = -gap
			}
			if gap > maxDist {
				continue
			}
			d := levenshteinDistance(longest, cand)
			if d < bestDist {
				bestDist = d
				bestIdx = i
			}
		}
		if bestIdx >= 0 && bestDist > 0 && bestDist <= maxDist {
			return fmt.Sprintf(" (nearest match: line %d: %s — re-read the file, it may have changed since your last read)", bestIdx+1, clipHintLine(origLines[bestIdx]))
		}
	}

	// 4. Keyword: longest token from first old line (min 3 chars so short
	// identifiers like "baz" still participate when the whole-line fuzzy
	// missed because surrounding context differed too much).
	if first := strings.TrimSpace(oldLines[0]); first != "" {
		keyword := ""
		for _, w := range strings.Fields(first) {
			// Strip common trailing punctuation so "foo()," still yields "foo".
			w = strings.Trim(w, ".,;:()[]{}\"'`")
			if len(w) > len(keyword) {
				keyword = w
			}
		}
		if len(keyword) >= 3 {
			for i, l := range origLines {
				if strings.Contains(l, keyword) {
					return fmt.Sprintf(" (nearest match: line %d: %s — re-read the file, it may have changed since your last read)", i+1, clipHintLine(l))
				}
			}
		}
	}
	return " (re-read the file — it may have changed since your last read)"
}

// matchTextForEdit returns a LF-normalized view of content for matching, plus
// whether the on-disk file used CRLF (so the write can restore it).
func matchTextForEdit(content string) (match string, crlf bool) {
	if strings.Contains(content, "\r\n") {
		return strings.ReplaceAll(content, "\r\n", "\n"), true
	}
	return content, false
}

func restoreEOL(content string, crlf bool) string {
	if !crlf {
		return content
	}
	// content is LF-only after edits; re-introduce CRLF.
	return strings.ReplaceAll(content, "\n", "\r\n")
}

// allIndex returns non-overlapping start offsets of sep in s.
func allIndex(s, sep string) []int {
	if sep == "" {
		return nil
	}
	var out []int
	for start := 0; start <= len(s); {
		i := strings.Index(s[start:], sep)
		if i < 0 {
			break
		}
		abs := start + i
		out = append(out, abs)
		start = abs + len(sep)
	}
	return out
}

// lineAtByte returns the 1-indexed line number of byte offset off in s (LF).
func lineAtByte(s string, off int) int {
	if off < 0 {
		off = 0
	}
	if off > len(s) {
		off = len(s)
	}
	return 1 + strings.Count(s[:off], "\n")
}

// editSuccessSnippet builds a short post-edit window around the first
// replacement so the model can confirm the change without a follow-up read.
//
// Hard caps (lines + bytes) are mandatory: replaceAll on a huge/single-line
// file must never dump the whole result. newText length must not expand the
// window — only a small fixed context around the first match start line.
// (Bug: endLine used to be startLine+len(newText lines), so a giant newText
// or replaceAll across a whole file set the window to EOF.)
const (
	editSnippetContext   = 2
	editSnippetMaxLines  = 12
	editSnippetMaxLineB  = 200 // per displayed line
	editSnippetMaxTotalB = 2 * 1024
)

func editSuccessSnippet(newContent string, firstStart int, _ string) string {
	if newContent == "" || firstStart < 0 {
		return ""
	}
	startLine := lineAtByte(newContent, firstStart) // 1-indexed
	// Inclusive 1-indexed window around the first replacement's start line.
	from := startLine - editSnippetContext
	if from < 1 {
		from = 1
	}
	to := startLine + editSnippetContext
	if to-from+1 > editSnippetMaxLines {
		to = from + editSnippetMaxLines - 1
	}

	var b strings.Builder
	pos := 0
	cur := 1
	for pos <= len(newContent) {
		nl := strings.IndexByte(newContent[pos:], '\n')
		var line string
		next := len(newContent)
		hasNL := nl >= 0
		if hasNL {
			line = newContent[pos : pos+nl]
			next = pos + nl + 1
		} else {
			line = newContent[pos:]
		}
		if cur >= from && cur <= to {
			if len(line) > editSnippetMaxLineB {
				line = utf8SafePrefix(line, editSnippetMaxLineB) + "…"
			}
			entry := fmt.Sprintf("%d: %s\n", cur, line)
			if b.Len()+len(entry) > editSnippetMaxTotalB {
				b.WriteString("… (snippet truncated)\n")
				break
			}
			b.WriteString(entry)
		}
		if cur >= to {
			break
		}
		if !hasNL {
			break
		}
		pos = next
		cur++
	}
	return strings.TrimRight(b.String(), "\n")
}

func (t *EditTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	rawPath, edits, err := parseEditInput(input)
	if err != nil {
		return ToolResult{Error: err.Error()}, nil
	}
	if rawPath == "" {
		return ToolResult{Error: "path is required"}, nil
	}
	if len(edits) == 0 {
		return ToolResult{Error: "no edits provided — pass edits: [{oldText, newText}, ...], or oldText/newText directly for a single edit"}, nil
	}

	path := resolvePath(t.cwd, rawPath)

	if !t.sandbox {
		if reason := guard.SensitivePathReason(path); reason != "" {
			allowed, denyReason := t.approvalFn(ctx, "edit "+path, reason, t.cwd)
			if !allowed {
				return ToolResult{Error: sensitivePathDenyMsg(reason, denyReason)}, nil
			}
		}
	}

	info, err := os.Stat(path)
	if err != nil {
		return ToolResult{Error: "cannot stat file: " + err.Error()}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ToolResult{Error: "cannot read file: " + err.Error()}, nil
	}
	originalRaw := string(data)
	matchText, crlf := matchTextForEdit(originalRaw)

	type replacement struct {
		start int
		end   int
		text  string
		idx   int
	}
	var repls []replacement
	totalRepl := 0
	for i, e := range edits {
		if e.OldText == "" {
			return ToolResult{Error: fmt.Sprintf("edit %d: oldText is empty", i)}, nil
		}
		if e.OldText == e.NewText {
			return ToolResult{Error: fmt.Sprintf("edit %d: oldText and newText are identical", i)}, nil
		}
		// Match against LF view; oldText from the model is almost always LF.
		old := strings.ReplaceAll(e.OldText, "\r\n", "\n")
		newT := strings.ReplaceAll(e.NewText, "\r\n", "\n")
		positions := allIndex(matchText, old)
		if len(positions) == 0 {
			return ToolResult{Error: fmt.Sprintf("edit %d: oldText not found in file%s", i, closestMatchHint(matchText, old))}, nil
		}
		if len(positions) > 1 && !e.ReplaceAll {
			return ToolResult{Error: fmt.Sprintf("edit %d: oldText is not unique (%d matches) — add more context to oldText, or set replaceAll: true", i, len(positions))}, nil
		}
		for _, start := range positions {
			repls = append(repls, replacement{start: start, end: start + len(old), text: newT, idx: i})
		}
		totalRepl += len(positions)
	}
	sort.Slice(repls, func(i, j int) bool {
		if repls[i].start != repls[j].start {
			return repls[i].start < repls[j].start
		}
		return repls[i].idx < repls[j].idx
	})
	for i := 1; i < len(repls); i++ {
		if repls[i].start < repls[i-1].end {
			return ToolResult{Error: fmt.Sprintf("edit %d overlaps edit %d", repls[i].idx, repls[i-1].idx)}, nil
		}
	}

	var b strings.Builder
	pos := 0
	firstNewStart := -1
	firstNewText := ""
	for _, r := range repls {
		b.WriteString(matchText[pos:r.start])
		if firstNewStart < 0 {
			firstNewStart = b.Len()
			firstNewText = r.text
		}
		b.WriteString(r.text)
		pos = r.end
	}
	b.WriteString(matchText[pos:])
	newLF := b.String()
	out := restoreEOL(newLF, crlf)

	if err := os.WriteFile(path, []byte(out), info.Mode().Perm()); err != nil {
		return ToolResult{Error: "cannot write file: " + err.Error()}, nil
	}

	startLine := 1
	if firstNewStart >= 0 {
		startLine = lineAtByte(newLF, firstNewStart)
	}
	snippet := editSuccessSnippet(newLF, firstNewStart, firstNewText)
	msg := fmt.Sprintf("edited %s (%d edit(s), %d replacement(s) applied, first @ line %d)", path, len(edits), totalRepl, startLine)
	if snippet != "" {
		msg += "\n" + snippet
	}
	return ToolResult{Content: msg}, nil
}
