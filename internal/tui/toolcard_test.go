package tui

import (
	"strings"
	"testing"
)

func TestToolCardLayout(t *testing.T) {
	b := Block{
		id:   1,
		kind: blockToolCall,
		meta: BlockMeta{
			ToolName: "bash",
			ToolInput: toolInputJSON("bash", map[string]string{
				"command": "git status",
			}),
			Streaming: true,
		},
	}
	rows := layoutToolCard(&b, 40, 0)
	if len(rows) < 3 {
		t.Fatalf("rows = %d", len(rows))
	}
	top := stripANSI(rows[0].Text)
	if !strings.Contains(top, "╭") || !strings.Contains(top, "bash") {
		t.Fatalf("header %q", top)
	}
	body := stripANSI(rows[1].Text)
	if !strings.Contains(body, "git status") {
		t.Fatalf("body %q", body)
	}
}

func TestToolCardComplete(t *testing.T) {
	s := newScrollback(1024)
	s.appendToolCall(1, "", "read", toolInputJSON("read", map[string]string{"path": "main.go"}))
	s.completeToolCall("", "package main", "", 400)
	b := s.blocks[0]
	if !b.meta.ToolDone || b.meta.ToolResult != "package main" {
		t.Fatalf("meta = %+v", b.meta)
	}
	rows := layoutToolCard(&b, 50, 0)
	last := stripANSI(rows[len(rows)-1].Text)
	if !strings.HasPrefix(last, "╰") {
		t.Fatalf("last row should be footer, got %q", last)
	}
	foundResult := false
	for _, row := range rows {
		plain := stripANSI(row.Text)
		if strings.Contains(plain, "✓") && strings.Contains(plain, "package main") {
			if !strings.HasPrefix(plain, "│") {
				t.Fatalf("result should be inside box: %q", plain)
			}
			foundResult = true
		}
	}
	if !foundResult {
		t.Fatal("expected boxed result row")
	}
}

func TestToolCardParallelPairingFIFO(t *testing.T) {
	s := newScrollback(1024)
	s.appendToolCall(0, "call_a", "read", toolInputJSON("read", map[string]string{"path": "a.go"}))
	s.appendToolCall(1, "call_b", "bash", toolInputJSON("bash", map[string]string{"command": "ls"}))
	s.completeToolCall("call_a", "READ_OUT", "", 0)
	if s.blocks[0].meta.ToolDone != true || s.blocks[0].meta.ToolResult != "READ_OUT" {
		t.Fatalf("block0 = %+v", s.blocks[0].meta)
	}
	if s.blocks[1].meta.ToolDone {
		t.Fatalf("block1 should still be open: %+v", s.blocks[1].meta)
	}
	s.completeToolCall("call_b", "BASH_OUT", "", 0)
	if s.blocks[1].meta.ToolResult != "BASH_OUT" {
		t.Fatalf("block1 = %+v", s.blocks[1].meta)
	}
}

func TestToolCardExpandHint(t *testing.T) {
	long := strings.Repeat("x", 500)
	b := Block{
		id:   4,
		kind: blockToolCall,
		meta: BlockMeta{
			ToolName:   "bash",
			ToolDone:   true,
			ToolResult: `{"stdout":"` + long + `","stderr":"","exitCode":0}`,
		},
	}
	rows := layoutToolCard(&b, 60, 0)
	foundHint := false
	for _, row := range rows {
		plain := stripANSI(row.Text)
		if strings.Contains(plain, "Ctrl+E") {
			if !strings.HasPrefix(plain, "│") {
				t.Fatalf("expand hint should be inside box: %q", plain)
			}
			foundHint = true
		}
	}
	if !foundHint {
		t.Fatal("expected expand hint inside tool card")
	}
}

func TestToolCardCollapsedResultInsideBox(t *testing.T) {
	b := Block{
		id:   6,
		kind: blockToolCall,
		meta: BlockMeta{
			ToolName:   "bash",
			ToolDone:   true,
			ToolResult: `{"stdout":"LONG BASH COMMAND EXTRAVAGANZA","stderr":"","exitCode":0}`,
		},
	}
	rows := layoutToolCard(&b, 60, 0)
	footerIdx := -1
	for i, row := range rows {
		if strings.HasPrefix(stripANSI(row.Text), "╰") {
			footerIdx = i
		}
	}
	if footerIdx < 1 {
		t.Fatal("expected footer row")
	}
	found := false
	for i := 1; i < footerIdx; i++ {
		plain := stripANSI(rows[i].Text)
		if strings.Contains(plain, "LONG BASH COMMAND") {
			if !strings.HasPrefix(plain, "│") {
				t.Fatalf("result outside box at row %d: %q", i, plain)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("expected collapsed result inside card")
	}
	for i := footerIdx + 1; i < len(rows); i++ {
		t.Fatalf("unexpected row after footer: %q", stripANSI(rows[i].Text))
	}
}

func TestToolCardExpandedLayout(t *testing.T) {
	var lines []string
	for i := 0; i < 30; i++ {
		lines = append(lines, "line "+strings.Repeat("y", 40))
	}
	b := Block{
		id:   5,
		kind: blockToolCall,
		meta: BlockMeta{
			ToolName:   "read",
			ToolDone:   true,
			ToolResult: strings.Join(lines, "\n"),
			Expanded:   true,
		},
	}
	expanded := layoutToolCard(&b, 50, 0)
	if len(expanded) < 10 {
		t.Fatalf("expanded rows = %d, want many", len(expanded))
	}
	b.meta.Expanded = false
	collapsed := layoutToolCard(&b, 50, 0)
	if len(collapsed) >= len(expanded) {
		t.Fatalf("collapsed %d should be fewer than expanded %d", len(collapsed), len(expanded))
	}
}

func TestToggleToolExpandInView(t *testing.T) {
	s := newScrollback(1024)
	long := strings.Repeat("z", 600)
	s.appendToolCall(1, "", "bash", toolInputJSON("bash", map[string]string{"command": "echo"}))
	s.completeToolCall("", `{"stdout":"`+long+`","stderr":"","exitCode":0}`, "", 10)
	if !s.toggleToolExpandInView(10, 50) {
		t.Fatal("toggle expand failed")
	}
	if !s.blocks[0].meta.Expanded {
		t.Fatal("expected expanded")
	}
	if !s.toggleToolExpandInView(10, 50) {
		t.Fatal("toggle collapse failed")
	}
	if s.blocks[0].meta.Expanded {
		t.Fatal("expected collapsed")
	}
}

func TestToolCardError(t *testing.T) {
	b := Block{
		id:   3,
		kind: blockToolCall,
		meta: BlockMeta{
			ToolName:  "bash",
			ToolDone:  true,
			ToolError: "permission denied",
		},
	}
	rows := layoutToolCard(&b, 40, 0)
	top := stripANSI(rows[0].Text)
	if !strings.Contains(top, "✗") {
		t.Fatalf("header %q", top)
	}
}
