package tui

import (
	"strings"
	"testing"
	"time"
)

func TestToolCardLayoutCollapsedBash(t *testing.T) {
	b := Block{
		id:   1,
		kind: blockToolCall,
		meta: BlockMeta{
			ToolName: "bash",
			ToolInput: toolInputJSON("bash", map[string]string{
				"command":     "git status",
				"description": "check git status",
			}),
			ToolDone:   true,
			DurationMs: 400,
		},
	}
	rows := layoutToolCard(&b, 60, 0)
	if len(rows) != 1 {
		t.Fatalf("collapsed bash should be 1 row, got %d", len(rows))
	}
	plain := stripANSI(rows[0].Text)
	if !strings.Contains(plain, "Bash") {
		t.Fatalf("header missing tool name: %q", plain)
	}
	if !strings.Contains(plain, "check git status") {
		t.Fatalf("header missing description: %q", plain)
	}
	if !strings.Contains(plain, "0.4s") {
		t.Fatalf("header missing duration: %q", plain)
	}
	// No yellow box borders.
	if strings.Contains(plain, "╭") || strings.Contains(plain, "│") || strings.Contains(plain, "╰") {
		t.Fatalf("collapsed card must not have box borders: %q", plain)
	}
}

func TestToolCardLayoutStreamingBash(t *testing.T) {
	// In-flight bash is still a single collapsed line (spinner + reason).
	b := Block{
		id:   1,
		kind: blockToolCall,
		meta: BlockMeta{
			ToolName: "bash",
			ToolInput: toolInputJSON("bash", map[string]string{
				"command":     "ls -la /tmp/secret-path-xyz",
				"description": "list temp dir",
			}),
			Streaming: true,
			StartedAt: time.Now(),
		},
	}
	rows := layoutToolCard(&b, 60, 0)
	if len(rows) != 1 {
		t.Fatalf("streaming bash should be 1 collapsed row, got %d", len(rows))
	}
	plain := stripANSI(rows[0].Text)
	if !strings.Contains(plain, "list temp dir") {
		t.Fatalf("streaming header missing description: %q", plain)
	}
	// Command itself stays hidden until expand.
	if strings.Contains(plain, "secret-path-xyz") {
		t.Fatalf("command should not leak into collapsed header: %q", plain)
	}
	if !strings.Contains(plain, toolCardSpinnerSlot) {
		t.Fatalf("streaming header missing spinner slot: %q", plain)
	}
}

// TestToolCardCollapsedShowsDurationEvenAtZero guards the display half of
// the approval-wait flicker fix (see pause_clock.go's blockElapsedMs doc
// comment for the root cause): a still-running collapsed card must show its
// "(Ns)" suffix even when the live elapsed value is exactly 0ms, not hide it
// — hiding it exactly at 0 is what turned an inherent ms-level rounding
// wobble into the whole suffix appearing/disappearing every render tick.
func TestToolCardCollapsedShowsDurationEvenAtZero(t *testing.T) {
	b := Block{
		id:   1,
		kind: blockToolCall,
		meta: BlockMeta{
			ToolName: "bash",
			ToolInput: toolInputJSON("bash", map[string]string{
				"command":     "rm -rf x",
				"description": "danger",
			}),
			Streaming: true,
			StartedAt: time.Now(), // elapsed ~0ms right now
		},
	}
	plain := stripANSI(formatToolCollapsed(&b, 60))
	if !strings.Contains(plain, "0.0s") {
		t.Fatalf("running card at ~0ms elapsed should still show a duration suffix (never hidden at exactly 0): %q", plain)
	}
}

func TestToolCardComplete(t *testing.T) {
	s := newScrollback(1024)
	s.appendToolCall(1, "", "read", toolInputJSON("read", map[string]string{"path": "main.go"}))
	s.completeToolCall("", "package main", "", "", 400)
	b := s.blocks[0]
	if !b.meta.ToolDone || b.meta.ToolResult != "package main" {
		t.Fatalf("meta = %+v", b.meta)
	}
	if b.meta.Expanded {
		t.Fatal("completed read should collapse")
	}
	rows := layoutToolCard(&b, 50, 0)
	if len(rows) != 1 {
		t.Fatalf("collapsed read = %d rows, want 1", len(rows))
	}
	plain := stripANSI(rows[0].Text)
	if !strings.Contains(plain, "Read") || !strings.Contains(plain, "main.go") {
		t.Fatalf("collapsed read header = %q", plain)
	}
}

func TestToolCardParallelPairingFIFO(t *testing.T) {
	s := newScrollback(1024)
	s.appendToolCall(0, "call_a", "read", toolInputJSON("read", map[string]string{"path": "a.go"}))
	s.appendToolCall(1, "call_b", "bash", toolInputJSON("bash", map[string]string{"command": "ls", "description": "list"}))
	s.completeToolCall("call_a", "READ_OUT", "", "", 0)
	if s.blocks[0].meta.ToolDone != true || s.blocks[0].meta.ToolResult != "READ_OUT" {
		t.Fatalf("block0 = %+v", s.blocks[0].meta)
	}
	if s.blocks[1].meta.ToolDone {
		t.Fatalf("block1 should still be open: %+v", s.blocks[1].meta)
	}
	s.completeToolCall("call_b", "BASH_OUT", "", "", 0)
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
			ToolInput:  toolInputJSON("bash", map[string]string{"command": "echo", "description": "long out"}),
		},
	}
	// Collapsed: single line, expandable.
	if !toolResultNeedsExpand(&b) {
		t.Fatal("long bash result should need expand")
	}
	rows := layoutToolCard(&b, 60, 0)
	if len(rows) != 1 {
		t.Fatalf("collapsed = %d rows", len(rows))
	}
}

func TestToolCardCollapsedIsSingleLine(t *testing.T) {
	b := Block{
		id:   6,
		kind: blockToolCall,
		meta: BlockMeta{
			ToolName:   "bash",
			ToolDone:   true,
			ToolResult: `{"stdout":"LONG BASH COMMAND EXTRAVAGANZA","stderr":"","exitCode":0}`,
			ToolInput:  toolInputJSON("bash", map[string]string{"command": "echo LONG", "description": "print long"}),
		},
	}
	rows := layoutToolCard(&b, 60, 0)
	if len(rows) != 1 {
		t.Fatalf("want 1 collapsed row, got %d", len(rows))
	}
	// Result body is hidden when collapsed — only the description shows.
	plain := stripANSI(rows[0].Text)
	if strings.Contains(plain, "LONG BASH COMMAND EXTRAVAGANZA") {
		t.Fatalf("result body leaked into collapsed header: %q", plain)
	}
	if !strings.Contains(plain, "print long") {
		t.Fatalf("description missing from collapsed header: %q", plain)
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
			ToolInput:  toolInputJSON("read", map[string]string{"path": "big.go"}),
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
	if len(collapsed) != 1 {
		t.Fatalf("collapsed = %d, want 1", len(collapsed))
	}
}

// TestBatchExpandedShowsPerCallDetails is the regression guard for the
// batch-card expand duplication: the body used to just repeat the header's
// "N calls: tool, tool, ..." summary verbatim instead of showing anything
// call-specific. Expanding now lists each call's own reason (subagent's
// task, read's path, ...).
func TestBatchExpandedShowsPerCallDetails(t *testing.T) {
	input := toolInputJSON("batch", map[string]interface{}{
		"calls": []map[string]interface{}{
			{"tool": "subagent", "input": map[string]string{"task": "explore checkout flow"}},
			{"tool": "read", "input": map[string]string{"path": "main.go"}},
		},
	})
	lines := toolExpandedInputLines("batch", input, 60)
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "1. subagent") || !strings.Contains(lines[0], "explore checkout flow") {
		t.Fatalf("line[0] = %q, want mention of subagent's task", lines[0])
	}
	if !strings.Contains(lines[1], "2. read") || !strings.Contains(lines[1], "main.go") {
		t.Fatalf("line[1] = %q, want mention of read's path", lines[1])
	}
	// Must differ from the collapsed header's generic summary, not repeat it.
	header := toolCollapsedReason("batch", input)
	joined := strings.Join(lines, "\n")
	if joined == header {
		t.Fatalf("expanded body duplicates the collapsed header verbatim: %q", header)
	}
}

func TestToggleToolExpandInView(t *testing.T) {
	s := newScrollback(1024)
	long := strings.Repeat("z", 600)
	s.appendToolCall(1, "", "bash", toolInputJSON("bash", map[string]string{"command": "echo", "description": "echo"}))
	s.completeToolCall("", `{"stdout":"`+long+`","stderr":"","exitCode":0}`, "", "", 10)
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
			ToolInput: toolInputJSON("bash", map[string]string{"command": "rm -rf /", "description": "wipe root"}),
		},
	}
	rows := layoutToolCard(&b, 60, 0)
	plain := stripANSI(rows[0].Text)
	if !strings.Contains(plain, "✗") {
		t.Fatalf("header %q", plain)
	}
}

func TestDiffToolAlwaysExpandedOnComplete(t *testing.T) {
	s := newScrollback(1024)
	s.appendToolCall(1, "", "write", toolInputJSON("write", map[string]any{
		"path":    "hello.go",
		"content": "package main\n",
	}))
	if !s.blocks[0].meta.Expanded {
		t.Fatal("write should start expanded")
	}
	s.completeToolCall("", "wrote hello.go", "", "", 5)
	if !s.blocks[0].meta.Expanded {
		t.Fatal("write must stay expanded after complete")
	}
	// Toggle must be a no-op (nothing to expand — always open, needsExpand=false).
	if s.toggleToolExpandInView(10, 60) {
		t.Fatal("diff tools should not toggle expand")
	}
}
