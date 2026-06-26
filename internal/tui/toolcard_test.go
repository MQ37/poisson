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
	s.appendToolCall(1, "read", toolInputJSON("read", map[string]string{"path": "main.go"}))
	s.completeToolCall("package main", "", 400)
	b := s.blocks[0]
	if !b.meta.ToolDone || b.meta.ToolResult != "package main" {
		t.Fatalf("meta = %+v", b.meta)
	}
	rows := b.layoutPlain(50)
	last := stripANSI(rows[len(rows)-1].Text)
	if !strings.Contains(last, "✓") {
		t.Fatalf("result %q", last)
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