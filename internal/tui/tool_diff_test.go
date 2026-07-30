package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditDiffLines(t *testing.T) {
	// No readable file → hunk-local fallback starting at 1.
	input := toolInputJSON("edit", map[string]any{
		"path": filepath.Join(t.TempDir(), "nope.go"),
		"edits": []map[string]string{
			{"oldText": "a\nb", "newText": "c"},
		},
	})
	lines := editDiffLines(input, "")
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

func TestEditDiffLinesAbsoluteFilePosition(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.go")
	// 5 preamble lines, then the oldText block starting at line 6.
	body := "package main\n\nimport \"fmt\"\n\nfunc main() {\n\told := 1\n\tfmt.Println(old)\n}\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	input := toolInputJSON("edit", map[string]any{
		"path": path,
		"edits": []map[string]string{
			{
				"oldText": "\told := 1\n\tfmt.Println(old)",
				"newText": "\tnew := 2\n\tfmt.Println(new)",
			},
		},
	})
	// Prefer the pre-edit snapshot (what appendToolCall stores as DiffBase).
	lines := editDiffLines(input, body)
	want := []diffLine{
		{sign: '-', text: "    old := 1", lineNo: 6},
		{sign: '-', text: "    fmt.Println(old)", lineNo: 7},
		{sign: '+', text: "    new := 2", lineNo: 6},
		{sign: '+', text: "    fmt.Println(new)", lineNo: 7},
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

// TestEditDiffLinesAbsoluteAfterFileAlreadyEdited is the steady-state path:
// the edit tool has already written newText to disk, so looking up oldText
// would miss. With a DiffBase snapshot we still get the right absolute lines;
// without one we fall back to locating newText in the post-edit file.
func TestEditDiffLinesAbsoluteAfterFileAlreadyEdited(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.go")
	pre := "package main\n\nimport \"fmt\"\n\nfunc main() {\n\told := 1\n\tfmt.Println(old)\n}\n"
	post := "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tnew := 2\n\tfmt.Println(new)\n}\n"
	if err := os.WriteFile(path, []byte(post), 0o644); err != nil {
		t.Fatal(err)
	}
	input := toolInputJSON("edit", map[string]any{
		"path": path,
		"edits": []map[string]string{
			{
				"oldText": "\told := 1\n\tfmt.Println(old)",
				"newText": "\tnew := 2\n\tfmt.Println(new)",
			},
		},
	})

	withBase := editDiffLines(input, pre)
	if withBase[0].lineNo != 6 {
		t.Fatalf("with DiffBase: first lineNo = %d, want 6", withBase[0].lineNo)
	}

	noBase := editDiffLines(input, "")
	if noBase[0].lineNo != 6 {
		t.Fatalf("newText fallback: first lineNo = %d, want 6", noBase[0].lineNo)
	}
}

func TestAppendToolCallSnapshotsDiffBase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.go")
	body := "one\ntwo\nthree\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newScrollback(1024)
	s.appendToolCall(1, "", "edit", toolInputJSON("edit", map[string]any{
		"path":    path,
		"oldText": "two",
		"newText": "TWO",
	}))
	if s.blocks[0].meta.DiffBase != body {
		t.Fatalf("DiffBase = %q, want pre-edit body", s.blocks[0].meta.DiffBase)
	}
	if err := os.WriteFile(path, []byte("one\nTWO\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if s.blocks[0].meta.DiffBase != body {
		t.Fatal("DiffBase must not change after disk mutation")
	}
}

func TestExpandTabsForDiffBG(t *testing.T) {
	// Tabs must become spaces before paint — a raw tab advances the cursor
	// without filling the active background, leaving a hole in the green/red.
	got := expandTabs("\treturn 1")
	if got != "    return 1" {
		t.Fatalf("expandTabs = %q", got)
	}
	lines := []diffLine{{sign: '+', text: "\treturn 1", lineNo: 10}}
	out := renderDiffLines(lines, 40, "go")
	if len(out) != 1 {
		t.Fatalf("rows = %d", len(out))
	}
	plain := stripANSI(out[0])
	if strings.Contains(plain, "\t") {
		t.Fatalf("rendered row still contains a raw tab: %q", plain)
	}
	if !strings.Contains(plain, "    return 1") {
		t.Fatalf("expected expanded spaces in %q", plain)
	}
	if bgDiffAdd == "" || !strings.Contains(out[0], bgDiffAdd) {
		t.Fatalf("missing bgDiffAdd in rendered row")
	}
}

func TestEditDiffLinesMultipleEditsSeparated(t *testing.T) {
	input := toolInputJSON("edit", map[string]any{
		"path": filepath.Join(t.TempDir(), "nope.go"),
		"edits": []map[string]string{
			{"oldText": "a", "newText": "b"},
			{"oldText": "x", "newText": "y"},
		},
	})
	lines := editDiffLines(input, "")
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
				"path": filepath.Join(os.TempDir(), "poisson-edit-card-missing.go"),
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
		if strings.Contains(plain, "│") && (strings.Contains(plain, "1 ") || strings.Contains(plain, " 1")) {
			sawLineNo = true
		}
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
		if strings.Contains(plain, "+ replacement") || strings.Contains(plain, "- nonexistent") {
			sawDiffMarker = true
		}
	}
	if !sawErrorText {
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
	s.completeToolCall("", "edited main.go (1 edit(s) applied)", "", "", 10)

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

// TestDiffToolLongPathExpandsToRevealFullPath is the regression test for the
// write/edit header truncating a long path with no way to ever see it: at a
// narrow width the header must truncate the path (not overflow it), the
// toggle must report "handled", and the untruncated path must show up on its
// own line once expanded.
func TestDiffToolLongPathExpandsToRevealFullPath(t *testing.T) {
	s := newScrollback(1024)
	longPath := "internal/some/deeply/nested/package/that/keeps/going/on/for/a/while/file.go"
	s.appendToolCall(1, "", "write", toolInputJSON("write", map[string]any{
		"path":    longPath,
		"content": "package foo\n",
	}))
	s.completeToolCall("", "wrote "+longPath, "", "", 10)

	width := 60
	rows := layoutToolCard(&s.blocks[0], width, 0)
	header := stripANSI(rows[0].Text)
	if strings.Contains(header, longPath) {
		t.Fatalf("header should truncate a path this long at width %d, got %q", width, header)
	}

	if !s.toggleToolExpandInView(10, width) {
		t.Fatal("expected toggle to report handled — the path is truncated, there's something to reveal")
	}
	if !s.blocks[0].meta.PathExpanded {
		t.Fatal("expected PathExpanded set after toggle")
	}

	// The full path is shown wrapped (a slash-only string has no break
	// points, so it hard-wraps across width-worth chunks with no separator)
	// — reassemble by stripping the "  " indent prefix from each line and
	// concatenating, rather than expecting it on a single row.
	rows = layoutToolCard(&s.blocks[0], width, 0)
	var joined strings.Builder
	for _, r := range rows[1:] {
		joined.WriteString(strings.TrimPrefix(stripANSI(r.Text), "  "))
	}
	if !strings.Contains(joined.String(), longPath) {
		t.Errorf("expected the full untruncated path to appear once expanded, got %q", joined.String())
	}

	// Toggle again: collapses back, full path line gone.
	if !s.toggleToolExpandInView(10, width) {
		t.Fatal("expected second toggle to also report handled (collapsing)")
	}
	if s.blocks[0].meta.PathExpanded {
		t.Fatal("expected PathExpanded cleared after second toggle")
	}
	rows = layoutToolCard(&s.blocks[0], width, 0)
	for _, r := range rows {
		if strings.Contains(stripANSI(r.Text), longPath) {
			t.Error("full path line should be gone after collapsing back")
		}
	}
}

func TestEditDiffLinesFlatShape(t *testing.T) {
	input := toolInputJSON("edit", map[string]any{
		"path":    filepath.Join(t.TempDir(), "missing.go"),
		"oldText": "a\nb",
		"newText": "c",
	})
	lines := editDiffLines(input, "")
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

func TestEditDiffLinesStringEncodedEdits(t *testing.T) {
	input := toolInputJSON("edit", map[string]any{
		"path":  filepath.Join(t.TempDir(), "missing.go"),
		"edits": `[{"oldText":"a","newText":"b"}]`,
	})
	lines := editDiffLines(input, "")
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
		"main.go":   "go",
		"x.py":      "python",
		"a.ts":      "typescript",
		"b.js":      "javascript",
		"c.json":    "json",
		"d.yml":     "yaml",
		"e.sh":      "bash",
		"noext":     "",
		"README.md": "text",
	}
	for path, want := range cases {
		if got := langFromPath(path); got != want {
			t.Errorf("langFromPath(%q) = %q, want %q", path, got, want)
		}
	}
}
