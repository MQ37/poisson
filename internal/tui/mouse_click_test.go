package tui

import (
	"strings"
	"testing"
)

func TestClickBlockAtThinking(t *testing.T) {
	s := newScrollback(1024)
	s.appendBlock(blockThinking, "thought")
	s.finalizeThinking()
	if !s.blocks[0].meta.Collapsed {
		t.Fatal("expected collapsed")
	}
	if !s.clickBlockAt(5, 40, 0) {
		t.Fatal("click failed")
	}
	if s.blocks[0].meta.Collapsed {
		t.Fatal("expected expanded")
	}
}

func TestClickBlockAtTool(t *testing.T) {
	s := newScrollback(1024)
	long := strings.Repeat("z", 600)
	s.appendToolCall(1, "", "bash", toolInputJSON("bash", map[string]string{"command": "echo"}))
	s.completeToolCall("", `{"stdout":"`+long+`","stderr":"","exitCode":0}`, "", 10)
	if !s.clickBlockAt(10, 50, 0) {
		t.Fatal("click expand failed")
	}
	if !s.blocks[0].meta.Expanded {
		t.Fatal("expected expanded")
	}
}

func TestClickBlockAtStreamingThinking(t *testing.T) {
	s := newScrollback(1024)
	s.appendBlock(blockThinking, "streaming")
	if s.clickBlockAt(5, 40, 0) {
		t.Fatal("should not toggle streaming thinking")
	}
}

func TestHandleMouseInputWheel(t *testing.T) {
	tui := newTUI(nil, "s1", nil)
	tui.scrollRows = 5
	tui.cols = 40
	tui.scroll.appendRaw(styleAssistant, strings.Repeat("word ", 200))
	tui.scroll.scrollToBottom()

	if !tui.handleMouseInput([]byte("\x1b[<64;10;5M")) {
		t.Fatal("wheel should be consumed")
	}
	if tui.scroll.scrollOffset != 3 {
		t.Fatalf("scrollOffset = %d, want 3", tui.scroll.scrollOffset)
	}
}

func TestHandleMouseClickScrollRegion(t *testing.T) {
	tui := newTUI(nil, "s1", nil)
	tui.headerRows = 1
	tui.scrollRows = 8
	tui.cols = 80
	tui.scroll.appendBlock(blockThinking, "thought")
	tui.scroll.finalizeThinking()

	tui.handleMouseClick(2) // row 2 = first scroll row when headerRows=1
	if tui.scroll.blocks[0].meta.Collapsed {
		t.Fatal("first click should expand")
	}
	tui.handleMouseClick(2)
	if !tui.scroll.blocks[0].meta.Collapsed {
		t.Fatal("second click should collapse")
	}
}

func TestApprovalMouseWheelScrollDirection(t *testing.T) {
	tui := newTUI(nil, "s1", nil)
	tui.approving.Store(true)
	var cmd strings.Builder
	for i := 0; i < 40; i++ {
		cmd.WriteString("echo line")
		cmd.WriteByte('\n')
	}
	ao := newApprovalOverlay(cmd.String(), "test", "")
	tui.activeOverlay = ao

	// Wheel up (btn 64) → earlier command lines → lower scroll index.
	if !tui.handleMouseInput([]byte("\x1b[<64;10;20M")) {
		t.Fatal("wheel should be consumed during approval")
	}
	if ao.scroll != 0 {
		t.Fatalf("wheel up at scroll=0 should stay 0, got %d", ao.scroll)
	}

	ao.scroll = 10
	if !tui.handleMouseInput([]byte("\x1b[<64;10;20M")) {
		t.Fatal("wheel up should be consumed")
	}
	if ao.scroll != 7 {
		t.Fatalf("wheel up: scroll = %d, want 7", ao.scroll)
	}

	// Wheel down (btn 65) → later command lines → higher scroll index.
	if !tui.handleMouseInput([]byte("\x1b[<65;10;20M")) {
		t.Fatal("wheel down should be consumed")
	}
	if ao.scroll != 10 {
		t.Fatalf("wheel down: scroll = %d, want 10", ao.scroll)
	}
}

func TestHandleMouseClickIgnoresHeader(t *testing.T) {
	tui := newTUI(nil, "s1", nil)
	tui.headerRows = 1
	tui.scrollRows = 8
	tui.cols = 80
	tui.scroll.appendBlock(blockThinking, "thought")
	tui.scroll.finalizeThinking()

	tui.handleMouseClick(1)
	if !tui.scroll.blocks[0].meta.Collapsed {
		t.Fatal("header click should be ignored")
	}
}