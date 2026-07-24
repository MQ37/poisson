package tui

import (
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/agent"
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
	ao := newApprovalOverlay(cmd.String(), "test", "", agent.ApprovalOriginMain)
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

// TestApprovalMouseWheelScrollsConversationAboveThePanel reproduces a
// reported bug: every wheel tick during a pending approval was routed to the
// approval panel's own (usually single-line) command scroll, even when the
// mouse was over the conversation above it — so scrolling the mouse wheel
// over the conversation while a prompt was pending silently did nothing,
// forcing users to reach for PgUp/PgDn instead (which already worked, see
// approvalRoutesToHandler). A wheel event whose row falls in the
// conversation region (above the panel) must scroll the conversation; one
// whose row falls on the panel itself must still scroll the panel.
func TestApprovalMouseWheelScrollsConversationAboveThePanel(t *testing.T) {
	tui := newTUI(nil, "s1", nil)
	tui.headerRows = 1
	tui.scrollRows = 5
	tui.cols = 40
	tui.scroll.appendRaw(styleAssistant, strings.Repeat("word ", 200))
	tui.scroll.scrollToBottom()
	tui.approving.Store(true)
	ao := newApprovalOverlay("echo hi", "test", "", agent.ApprovalOriginMain)
	tui.activeOverlay = ao

	// Row 3 sits inside the conversation region (rows 2..6 = headerRows+1 ..
	// headerRows+scrollRows here) — must scroll the conversation, not the panel.
	if !tui.handleMouseInput([]byte("\x1b[<64;10;3M")) {
		t.Fatal("wheel should be consumed")
	}
	if tui.scroll.scrollOffset != 3 {
		t.Fatalf("conversation scrollOffset = %d, want 3", tui.scroll.scrollOffset)
	}
	if ao.scroll != 0 {
		t.Fatalf("panel scroll should be untouched, got %d", ao.scroll)
	}

	// Row 7 sits on the panel (panelTop = headerRows+scrollRows+1 = 7) — must
	// still scroll the panel, unaffected by the conversation scroll above.
	if !tui.handleMouseInput([]byte("\x1b[<65;10;7M")) {
		t.Fatal("wheel should be consumed")
	}
	if ao.scroll != 3 {
		t.Fatalf("panel scroll = %d, want 3", ao.scroll)
	}
	if tui.scroll.scrollOffset != 3 {
		t.Fatalf("conversation scrollOffset should stay 3, got %d", tui.scroll.scrollOffset)
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

// TestFocusedToolMouseWheelScrollDirection reproduces a reported bug: wheel
// scroll inside an expanded tool card (read/skill/etc.) moved the opposite
// way from the main conversation and the approval overlay (see
// TestApprovalMouseWheelScrollDirection above) — wheel up must reveal
// earlier lines, wheel down later ones, everywhere wheel scroll works.
func TestFocusedToolMouseWheelScrollDirection(t *testing.T) {
	tui := newTUI(nil, "s1", nil)
	tui.cols = 40
	long := strings.Repeat("line\n", 100)
	tui.scroll.appendToolCall(1, "", "read", toolInputJSON("read", map[string]string{"path": "f"}))
	tui.scroll.completeToolCall("", `{"stdout":"`+long+`","stderr":"","exitCode":0}`, "", 10)
	tui.scroll.blocks[0].meta.Expanded = true
	tui.scroll.focusedToolID = tui.scroll.blocks[0].id
	tui.scroll.blocks[0].meta.ToolScroll = 10

	// Wheel up (btn 64) → earlier lines → lower ToolScroll (same convention
	// as the approval overlay and the main scrollback).
	if !tui.handleMouseInput([]byte("\x1b[<64;10;5M")) {
		t.Fatal("wheel up should be consumed")
	}
	if got := tui.scroll.blocks[0].meta.ToolScroll; got != 7 {
		t.Fatalf("wheel up: ToolScroll = %d, want 7", got)
	}

	// Wheel down (btn 65) → later lines → higher ToolScroll.
	if !tui.handleMouseInput([]byte("\x1b[<65;10;5M")) {
		t.Fatal("wheel down should be consumed")
	}
	if got := tui.scroll.blocks[0].meta.ToolScroll; got != 10 {
		t.Fatalf("wheel down: ToolScroll = %d, want 10", got)
	}
}
