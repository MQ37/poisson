package tui

import (
	"strings"
	"testing"
)

func TestEditDiffLines(t *testing.T) {
	input := toolInputJSON("edit", map[string]any{
		"path": "main.go",
		"edits": []map[string]string{
			{"oldText": "a\nb", "newText": "c"},
		},
	})
	lines := editDiffLines(input)
	want := []diffLine{
		{sign: '-', text: "a", lineNo: 1},
		{sign: '-', text: "b", lineNo: 2},
		{sign: '+', text: "c", lineNo: 1},
	}
	if len(lines) != len(want) {
		t.Fatalf("lines = %+v, want %+v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %+v, want %+v", i, lines[i], want[i])
		}
	}
}

func TestEditDiffLinesMultipleEditsSeparated(t *testing.T) {
	input := toolInputJSON("edit", map[string]any{
		"path": "main.go",
		"edits": []map[string]string{
			{"oldText": "a", "newText": "b"},
			{"oldText": "x", "newText": "y"},
		},
	})
	lines := editDiffLines(input)
	// -a +b <blank separator> -x +y
	if len(lines) != 5 {
		t.Fatalf("lines = %+v, want 5 (2 + separator + 2)", lines)
	}
	if lines[2].sign != ' ' {
		t.Errorf("expected blank separator between edits, got %+v", lines[2])
	}
}

func TestWriteDiffLinesAllAdded(t *testing.T) {
	input := toolInputJSON("write", map[string]any{
		"path":    "hello.go",
		"content": "line1\nline2",
	})
	lines := writeDiffLines(input)
	want := []diffLine{
		{sign: '+', text: "line1", lineNo: 1},
		{sign: '+', text: "line2", lineNo: 2},
	}
	if len(lines) != len(want) {
		t.Fatalf("lines = %+v, want %+v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %+v, want %+v", i, lines[i], want[i])
		}
	}
}

func TestIsDiffTool(t *testing.T) {
	for _, name := range []string{"edit", "write"} {
		if !isDiffTool(name) {
			t.Errorf("isDiffTool(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"bash", "read", "search", ""} {
		if isDiffTool(name) {
			t.Errorf("isDiffTool(%q) = true, want false", name)
		}
	}
}

func editCardBlock() Block {
	return Block{
		id:   1,
		kind: blockToolCall,
		meta: BlockMeta{
			ToolName: "edit",
			ToolDone: true,
			Expanded: true,
			ToolInput: toolInputJSON("edit", map[string]any{
				"path": "main.go",
				"edits": []map[string]string{
					{"oldText": "func old() {\n\treturn 1\n}", "newText": "func new() {\n\treturn 2\n}"},
				},
			}),
			ToolResult: "edited main.go (1 edit(s) applied)",
		},
	}
}

func TestToolCardEditShowsColoredDiffAlways(t *testing.T) {
	b := editCardBlock()
	rows := layoutToolCard(&b, 80, 0)
	var sawRed, sawGreen, sawLineNo bool
	for _, r := range rows {
		plain := stripANSI(r.Text)
		if strings.Contains(r.Text, bgDiffDel) && strings.Contains(plain, "func old") {
			sawRed = true
		}
		if strings.Contains(r.Text, bgDiffAdd) && strings.Contains(plain, "func new") {
			sawGreen = true
		}
		// Line numbers present (e.g. " 1 │").
		if strings.Contains(plain, "│") && (strings.Contains(plain, "1 ") || strings.Contains(plain, " 1")) {
			sawLineNo = true
		}
		// No yellow box borders.
		if strings.Contains(plain, "╭") || strings.Contains(plain, "╰") {
			t.Errorf("diff card must not have box borders: %q", plain)
		}
	}
	if !sawRed {
		t.Error("expected a red-bg '-' line for the old text")
	}
	if !sawGreen {
		t.Error("expected a green-bg '+' line for the new text")
	}
	if !sawLineNo {
		t.Error("expected line numbers in the diff")
	}
}

func TestToolCardWriteShowsColoredDiff(t *testing.T) {
	b := Block{
		id:   2,
		kind: blockToolCall,
		meta: BlockMeta{
			ToolName: "write",
			ToolDone: true,
			Expanded: true,
			ToolInput: toolInputJSON("write", map[string]any{
				"path":    "hello.go",
				"content": "package main\n\nfunc main() {}\n",
			}),
			ToolResult: "wrote hello.go",
		},
	}
	rows := layoutToolCard(&b, 80, 0)
	var sawGreen bool
	for _, r := range rows {
		plain := stripANSI(r.Text)
		if strings.Contains(r.Text, bgDiffAdd) && strings.Contains(plain, "package main") {
			sawGreen = true
		}
		if strings.Contains(r.Text, bgDiffDel) {
			t.Errorf("write diff should never show red (removed) lines: %q", plain)
		}
	}
	if !sawGreen {
		t.Error("expected a green-bg '+' line for the written content")
	}
}

func TestToolCardDiffErrorFallsBackToPlainError(t *testing.T) {
	b := Block{
		id:   3,
		kind: blockToolCall,
		meta: BlockMeta{
			ToolName:  "edit",
			ToolDone:  true,
			ToolError: "edit 0: oldText not found in file",
			ToolInput: toolInputJSON("edit", map[string]any{
				"path": "main.go",
				"edits": []map[string]string{
					{"oldText": "nonexistent", "newText": "replacement"},
				},
			}),
		},
	}
	rows := layoutToolCard(&b, 60, 0)
	var sawErrorText, sawDiffMarker bool
	for _, r := range rows {
		plain := stripANSI(r.Text)
		if strings.Contains(plain, "oldText not found") || strings.Contains(plain, "edit 0") {
			sawErrorText = true
		}
		// Failed edit falls through to compact/error path, not the full diff.
		if strings.Contains(plain, "+ replacement") || strings.Contains(plain, "- nonexistent") {
			sawDiffMarker = true
		}
	}
	if !sawErrorText {
		// Compact collapsed line uses the reason (path), not the error body —
		// but the ✗ mark must appear.
		plain := stripANSI(rows[0].Text)
		if !strings.Contains(plain, "✗") {
			t.Errorf("expected error indicator, got %q", plain)
		}
	}
	if sawDiffMarker {
		t.Error("a failed edit never touched the file — should not render a diff")
	}
}

func TestDiffToolNoExpandToggle(t *testing.T) {
	s := newScrollback(1024)
	s.appendToolCall(1, "", "edit", toolInputJSON("edit", map[string]any{
		"path": "main.go",
		"edits": []map[string]string{
			{"oldText": "func old() {\n\treturn 1\n}", "newText": "func new() {\n\treturn 2\n}"},
		},
	}))
	s.completeToolCall("", "edited main.go (1 edit(s) applied)", "", 10)

	// Always expanded; toggle is a no-op.
	if !s.blocks[0].meta.Expanded {
		t.Fatal("edit must be expanded")
	}
	if s.toggleToolExpandInView(10, 60) {
		t.Fatal("diff tool expand toggle should fail (always open, nothing to expand)")
	}
	rows := layoutToolCard(&s.blocks[0], 80, 0)
	var sawNewGreen bool
	for _, r := range rows {
		if strings.Contains(stripANSI(r.Text), "func new") {
			sawNewGreen = true
		}
	}
	if !sawNewGreen {
		t.Error("card should show the full diff including the added side")
	}
}

// TestEditDiffLinesFlatShape reproduces a reported regression: after
// internal/tools/edit.go's Execute started accepting a flat top-level
// {path, oldText, newText} shape (d3b6ffd), this package's own independent
// re-parse of the same input JSON (for the colored diff card) was never
// updated to match — it only ever recognized edits: [{...}]. A tool card
// using the flat shape rendered as an empty diff ("0 edits").
func TestEditDiffLinesFlatShape(t *testing.T) {
	input := toolInputJSON("edit", map[string]any{
		"path":    "main.go",
		"oldText": "a\nb",
		"newText": "c",
	})
	lines := editDiffLines(input)
	want := []diffLine{
		{sign: '-', text: "a", lineNo: 1},
		{sign: '-', text: "b", lineNo: 2},
		{sign: '+', text: "c", lineNo: 1},
	}
	if len(lines) != len(want) {
		t.Fatalf("lines = %+v, want %+v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %+v, want %+v", i, lines[i], want[i])
		}
	}
}

// TestEditDiffLinesStringEncodedEdits mirrors the tool's own recovery for a
// double-encoded edits array (edits sent as a JSON string instead of an
// array) — the diff card must recover it the same way Execute does.
func TestEditDiffLinesStringEncodedEdits(t *testing.T) {
	input := toolInputJSON("edit", map[string]any{
		"path":  "main.go",
		"edits": `[{"oldText":"a","newText":"b"}]`,
	})
	lines := editDiffLines(input)
	want := []diffLine{
		{sign: '-', text: "a", lineNo: 1},
		{sign: '+', text: "b", lineNo: 1},
	}
	if len(lines) != len(want) {
		t.Fatalf("lines = %+v, want %+v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %+v, want %+v", i, lines[i], want[i])
		}
	}
}

func TestRenderDiffLinesHaveLineNumbersAndBG(t *testing.T) {
	if bgDiffAdd == "" || bgDiffDel == "" {
		t.Fatal("bgDiffAdd/bgDiffDel must be non-empty under the active theme")
	}
	lines := []diffLine{
		{sign: '-', text: "old", lineNo: 1},
		{sign: '+', text: "new", lineNo: 1},
	}
	out := renderDiffLines(lines, 40, "go")
	if len(out) < 2 {
		t.Fatalf("rows = %d", len(out))
	}
	if !strings.Contains(out[0], bgDiffDel) {
		t.Error("del line missing bgDiffDel")
	}
	if !strings.Contains(out[1], bgDiffAdd) {
		t.Error("add line missing bgDiffAdd")
	}
	plain0 := stripANSI(out[0])
	if !strings.Contains(plain0, "1") || !strings.Contains(plain0, "│") {
		t.Errorf("expected line number + bar in %q", plain0)
	}
	// Highlight resets must not kill the bg: every reset in the body is
	// followed by a re-apply of the row's background.
	body := out[1]
	// Count occurrences of reset not immediately followed by bgDiffAdd —
	// the final trailing reset is OK (ends the row).
	idx := 0
	for {
		i := strings.Index(body[idx:], reset)
		if i < 0 {
			break
		}
		i += idx
		after := i + len(reset)
		if after >= len(body) {
			break // trailing reset at end of row — fine
		}
		if !strings.HasPrefix(body[after:], bgDiffAdd) {
			t.Fatalf("reset at %d not followed by bgDiffAdd — bg would drop mid-line", i)
		}
		idx = after
	}
}

func TestLangFromPath(t *testing.T) {
	cases := map[string]string{
		"main.go":      "go",
		"x.py":         "python",
		"a.ts":         "typescript",
		"b.js":         "javascript",
		"c.json":       "json",
		"d.yml":        "yaml",
		"e.sh":         "bash",
		"noext":        "",
		"README.md":    "text",
	}
	for path, want := range cases {
		if got := langFromPath(path); got != want {
			t.Errorf("langFromPath(%q) = %q, want %q", path, got, want)
		}
	}
}
