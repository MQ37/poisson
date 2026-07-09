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
		{sign: '-', text: "a"},
		{sign: '-', text: "b"},
		{sign: '+', text: "c"},
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
		{sign: '+', text: "line1"},
		{sign: '+', text: "line2"},
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

func editCardBlock(expanded bool) Block {
	return Block{
		id:   1,
		kind: blockToolCall,
		meta: BlockMeta{
			ToolName: "edit",
			ToolDone: true,
			Expanded: expanded,
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

func TestToolCardEditShowsColoredDiffCollapsed(t *testing.T) {
	b := editCardBlock(false)
	rows := layoutToolCard(&b, 60, 0)
	var sawRed, sawGreen bool
	for _, r := range rows {
		if strings.Contains(r.Text, fgRed) && strings.Contains(stripANSI(r.Text), "- func old") {
			sawRed = true
		}
		if strings.Contains(r.Text, fgGreen) && strings.Contains(stripANSI(r.Text), "+ func new") {
			sawGreen = true
		}
	}
	if !sawRed {
		t.Error("expected a red '-' line for the old text")
	}
	if !sawGreen {
		// Collapsed view only shows the first 3 lines; the old text alone is
		// 3 lines, so the added side isn't visible yet \u2014 assert the expand
		// hint is offered instead so the user knows there's more.
		found := false
		for _, r := range rows {
			if strings.Contains(stripANSI(r.Text), "click/Ctrl+E") {
				found = true
			}
		}
		if !found {
			t.Error("collapsed diff hides the added side but offers no expand hint")
		}
	}
}

func TestToolCardEditShowsColoredDiffExpanded(t *testing.T) {
	b := editCardBlock(true)
	rows := layoutToolCard(&b, 60, 0)
	var sawOldRed, sawNewGreen bool
	for _, r := range rows {
		plain := stripANSI(r.Text)
		if strings.Contains(r.Text, fgRed) && strings.Contains(plain, "- func old") {
			sawOldRed = true
		}
		if strings.Contains(r.Text, fgGreen) && strings.Contains(plain, "+ func new") {
			sawNewGreen = true
		}
	}
	if !sawOldRed {
		t.Error("expanded diff missing red old-text line")
	}
	if !sawNewGreen {
		t.Error("expanded diff missing green new-text line")
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
	rows := layoutToolCard(&b, 60, 0)
	var sawGreen bool
	for _, r := range rows {
		if strings.Contains(r.Text, fgGreen) && strings.Contains(stripANSI(r.Text), "+ package main") {
			sawGreen = true
		}
		if strings.Contains(r.Text, fgRed) {
			t.Errorf("write diff should never show red (removed) lines: %q", stripANSI(r.Text))
		}
	}
	if !sawGreen {
		t.Error("expected a green '+' line for the written content")
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
		if strings.Contains(plain, "oldText not found") {
			sawErrorText = true
		}
		if strings.Contains(plain, "+ replacement") || strings.Contains(plain, "- nonexistent") {
			sawDiffMarker = true
		}
	}
	if !sawErrorText {
		t.Error("expected the plain error message to be shown")
	}
	if sawDiffMarker {
		t.Error("a failed edit never touched the file \u2014 should not render a diff")
	}
}

func TestToggleToolExpandDiffCard(t *testing.T) {
	s := newScrollback(1024)
	s.appendToolCall(1, "", "edit", toolInputJSON("edit", map[string]any{
		"path": "main.go",
		"edits": []map[string]string{
			{"oldText": "func old() {\n\treturn 1\n}", "newText": "func new() {\n\treturn 2\n}"},
		},
	}))
	s.completeToolCall("", "edited main.go (1 edit(s) applied)", "", 10)

	if !s.toggleToolExpandInView(10, 60) {
		t.Fatal("expected expand toggle to succeed for a multi-line diff")
	}
	if !s.blocks[0].meta.Expanded {
		t.Fatal("expected block to be expanded")
	}
	rows := layoutToolCard(&s.blocks[0], 60, 0)
	var sawNewGreen bool
	for _, r := range rows {
		if strings.Contains(stripANSI(r.Text), "+ func new") {
			sawNewGreen = true
		}
	}
	if !sawNewGreen {
		t.Error("expanded card should show the full diff including the added side")
	}
}
