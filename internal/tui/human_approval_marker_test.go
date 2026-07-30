package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/agent"
	"github.com/mq37/poisson/internal/tools"
)

// TestApproveRecordsApprovalDecision verifies TUI.Approve reports its
// outcome into ctx's ApprovalRecord — the single choke point that lets any
// current or future ApprovalFn/FileApprovalFn/SandboxApprovalFn caller get
// the conversation view's marker with no per-tool wiring.
func TestApproveRecordsApprovalDecision(t *testing.T) {
	tui := newTestTUIHelper()
	ctx, rec := tools.WithApprovalRecord(context.Background())

	result := make(chan bool, 1)
	go func() {
		allowed, _ := tui.Approve(ctx, "ls", "list", "", agent.BashRiskLow, agent.ApprovalOriginMain)
		result <- allowed
	}()

	deadline := time.Now().Add(500 * time.Millisecond)
	for !tui.approving.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	tui.approvalAnswer <- approvalReply{Allowed: true}

	select {
	case <-result:
	case <-time.After(time.Second):
		t.Fatal("Approve timed out")
	}
	if !rec.Asked || !rec.Allowed {
		t.Fatalf("record = %+v, want {Asked:true Allowed:true}", rec)
	}
}

// TestApproveRecordsDenialDecision is the denied counterpart.
func TestApproveRecordsDenialDecision(t *testing.T) {
	tui := newTestTUIHelper()
	ctx, rec := tools.WithApprovalRecord(context.Background())

	result := make(chan bool, 1)
	go func() {
		allowed, _ := tui.Approve(ctx, "rm -rf x", "danger", "", agent.BashRiskHigh, agent.ApprovalOriginMain)
		result <- allowed
	}()

	deadline := time.Now().Add(500 * time.Millisecond)
	for !tui.approving.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	tui.approvalAnswer <- approvalReply{Allowed: false}

	select {
	case <-result:
	case <-time.After(time.Second):
		t.Fatal("Approve timed out")
	}
	if !rec.Asked || rec.Allowed {
		t.Fatalf("record = %+v, want {Asked:true Allowed:false}", rec)
	}
}

// TestApproveNoRecordAttachedIsSafe is the /btw-style case: a plain
// background context (no ApprovalRecord attached) must not panic.
func TestApproveNoRecordAttachedIsSafe(t *testing.T) {
	tui := newTestTUIHelper()
	result := make(chan bool, 1)
	go func() {
		allowed, _ := tui.Approve(context.Background(), "ls", "list", "", agent.BashRiskLow, agent.ApprovalOriginMain)
		result <- allowed
	}()

	deadline := time.Now().Add(500 * time.Millisecond)
	for !tui.approving.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	tui.approvalAnswer <- approvalReply{Allowed: true}

	select {
	case <-result:
	case <-time.After(time.Second):
		t.Fatal("Approve timed out")
	}
}

// --- Rendering ---------------------------------------------------------

func TestHumanApprovalGlyph(t *testing.T) {
	cases := []struct {
		humanApproval string
		wantGlyph     string
		wantAbsent    []string
	}{
		{"approved", "●", []string{"○"}},
		{"denied", "○", []string{"●"}},
		{"", "", []string{"●", "○"}},
	}
	for _, tc := range cases {
		got := humanApprovalGlyph(tc.humanApproval)
		plain := stripANSI(got)
		if tc.wantGlyph == "" {
			if plain != "" {
				t.Errorf("humanApprovalGlyph(%q) = %q, want empty", tc.humanApproval, plain)
			}
			continue
		}
		if !strings.Contains(plain, tc.wantGlyph) {
			t.Errorf("humanApprovalGlyph(%q) = %q, want to contain %q", tc.humanApproval, plain, tc.wantGlyph)
		}
		for _, absent := range tc.wantAbsent {
			if strings.Contains(plain, absent) {
				t.Errorf("humanApprovalGlyph(%q) = %q, must not contain %q", tc.humanApproval, plain, absent)
			}
		}
	}
}

// TestToolCardCollapsedShowsApprovalMarker verifies the collapsed bash/read
// header (the common case for a non-diff tool) carries the marker.
func TestToolCardCollapsedShowsApprovalMarker(t *testing.T) {
	b := Block{
		id:   1,
		kind: blockToolCall,
		meta: BlockMeta{
			ToolName: "bash",
			ToolInput: toolInputJSON("bash", map[string]string{
				"command":     "rm -rf x",
				"description": "cleanup",
			}),
			ToolDone:      true,
			ToolError:     "command rejected by user",
			HumanApproval: "denied",
		},
	}
	rows := layoutToolCard(&b, 60, 0)
	plain := stripANSI(rows[0].Text)
	if !strings.Contains(plain, "○") {
		t.Fatalf("expected denied marker in collapsed header: %q", plain)
	}
}

// TestToolCardExpandedShowsApprovalMarker checks formatToolExpandedHeader
// (the toggled-open view of the same card).
func TestToolCardExpandedShowsApprovalMarker(t *testing.T) {
	b := &Block{
		meta: BlockMeta{
			ToolName:      "bash",
			ToolInput:     toolInputJSON("bash", map[string]string{"command": "ls", "description": "look"}),
			ToolDone:      true,
			HumanApproval: "approved",
		},
	}
	got := stripANSI(formatToolExpandedHeader(b))
	if !strings.Contains(got, "●") {
		t.Fatalf("expected approved marker in expanded header: %q", got)
	}
}

// TestDiffToolHeaderShowsApprovalMarker checks edit/write's always-expanded
// header, the third of the three header formatters.
func TestDiffToolHeaderShowsApprovalMarker(t *testing.T) {
	b := &Block{
		meta: BlockMeta{
			ToolName:      "write",
			ToolInput:     toolInputJSON("write", map[string]any{"path": "hello.go", "content": "package main\n"}),
			ToolDone:      true,
			HumanApproval: "approved",
		},
	}
	got := stripANSI(formatDiffToolHeader(b, 60))
	if !strings.Contains(got, "●") {
		t.Fatalf("expected approved marker in diff header: %q", got)
	}
}

// TestToolCardNoMarkerWhenNeverAsked is the common silent case: an
// auto-approved (or never-gated) tool call shows neither glyph.
func TestToolCardNoMarkerWhenNeverAsked(t *testing.T) {
	b := Block{
		id:   1,
		kind: blockToolCall,
		meta: BlockMeta{
			ToolName:  "bash",
			ToolInput: toolInputJSON("bash", map[string]string{"command": "ls", "description": "list"}),
			ToolDone:  true,
		},
	}
	rows := layoutToolCard(&b, 60, 0)
	plain := stripANSI(rows[0].Text)
	if strings.Contains(plain, "●") || strings.Contains(plain, "○") {
		t.Fatalf("expected no approval marker when never asked: %q", plain)
	}
}

// TestCompleteToolCallSetsHumanApproval verifies the field flows from
// scrollback.completeToolCall through to the block, end to end with the
// dispatch loop's OutputEvent shape.
func TestCompleteToolCallSetsHumanApproval(t *testing.T) {
	s := newScrollback(1024)
	s.appendToolCall(1, "call-1", "bash", toolInputJSON("bash", map[string]string{"command": "ls", "description": "list"}))
	s.completeToolCall("call-1", "ok", "", "approved", 5)
	if s.blocks[0].meta.HumanApproval != "approved" {
		t.Fatalf("HumanApproval = %q, want approved", s.blocks[0].meta.HumanApproval)
	}
}
