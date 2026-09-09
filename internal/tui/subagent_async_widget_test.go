package tui

import (
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/agent"
)

func TestSubagentJobIDFromAck(t *testing.T) {
	jobID, ok := subagentJobIDFromAck(`Subagent "scout" spawned as job sub-a1b2c3d4. It runs in the background — use subagent_status to check progress, subagent_result to retrieve the final output once done.`)
	if !ok || jobID != "sub-a1b2c3d4" {
		t.Fatalf("got (%q, %v), want (sub-a1b2c3d4, true)", jobID, ok)
	}
}

func TestSubagentJobIDFromAck_RealResultIsNotAck(t *testing.T) {
	// A finished job's own result text ("Subagent finished. N tool calls...")
	// must never be mistaken for an ack, or a real completion would loop
	// back into "still spawning" instead of actually completing the widget.
	if _, ok := subagentJobIDFromAck("did the thing\n\n---\nSubagent finished. 2 tool calls, 3 turns. Ran on anthropic/claude-opus-5."); ok {
		t.Fatal("a real result text was misidentified as a spawn ack")
	}
}

func TestSetSubagentJobID_RecordsWithoutCompleting(t *testing.T) {
	s := newScrollback(1024)
	s.appendSubagentCard(1, "call-1", "scout", "look around", "anthropic/claude-opus-5")

	if !s.setSubagentJobID("call-1", "sub-xyz") {
		t.Fatal("setSubagentJobID did not match the running widget")
	}
	if s.blocks[0].meta.SubagentJobID != "sub-xyz" {
		t.Fatalf("SubagentJobID = %q, want sub-xyz", s.blocks[0].meta.SubagentJobID)
	}
	if s.blocks[0].meta.ToolDone {
		t.Fatal("recording the job id must not complete the widget")
	}
}

func TestSetSubagentJobID_NoMatchReturnsFalse(t *testing.T) {
	s := newScrollback(1024)
	if s.setSubagentJobID("nope", "sub-xyz") {
		t.Fatal("expected no match on an empty scrollback")
	}
}

func TestCompleteSubagentCard_MatchesByJobIDAfterAck(t *testing.T) {
	s := newScrollback(1024)
	s.appendSubagentCard(1, "call-1", "scout", "look around", "anthropic/claude-opus-5")
	s.setSubagentJobID("call-1", "sub-xyz")

	// The real completion arrives keyed by job id, not the original
	// tool-call id — exactly what Agent.CompleteSubagentJob sends.
	if !s.completeSubagentCard("sub-xyz", "did the thing\n\n---\nSubagent finished. 1 tool calls, 1 turns. Ran on anthropic/claude-opus-5.", "", 0) {
		t.Fatal("completeSubagentCard did not match by job id")
	}
	if !s.blocks[0].meta.ToolDone {
		t.Fatal("widget should be done after the job-id-keyed completion")
	}
}

// TestCompleteSubagentCard_BatchedCallSameAckThenFinalPattern proves the
// batched nested-subagent path (agent.go pre-renders with a synthetic
// BatchCallID, batch.go's SetSubagentDoneFn -> Agent.CompleteBatchedSubagent
// pushes the SAME OutputToolResult shape as a direct call) needs no special
// casing at all: it's ack-detected and job-id-matched identically, since
// both paths converge on the exact same TUI routing.
func TestCompleteSubagentCard_BatchedCallSameAckThenFinalPattern(t *testing.T) {
	s := newScrollback(1024)
	s.appendSubagentCard(1, "call-outer.0", "scout", "look around", "anthropic/claude-opus-5")

	ack := `Subagent "scout" spawned as job sub-batched1. It runs in the background — use subagent_status to check progress, subagent_result to retrieve the final output once done.`
	jobID, ok := subagentJobIDFromAck(ack)
	if !ok {
		t.Fatal("ack not recognized")
	}
	if !s.setSubagentJobID("call-outer.0", jobID) {
		t.Fatal("setSubagentJobID did not match the batched widget")
	}
	if s.blocks[0].meta.ToolDone {
		t.Fatal("batched widget completed on the ack alone")
	}

	if !s.completeSubagentCard(jobID, "did the thing\n\n---\nSubagent finished. 1 tool calls, 1 turns.", "", 0) {
		t.Fatal("completeSubagentCard did not match the batched widget by job id")
	}
	if !s.blocks[0].meta.ToolDone {
		t.Fatal("batched widget should be done after the job-id-keyed completion")
	}
}

// TestHandleEvent_SubagentAckDoesNotCompleteWidget drives the full pipeline
// a live turn actually uses (TUI.handleEvent, not the scrollback methods
// directly) to prove the generic per-tool OutputToolResult dispatch — which
// fires unconditionally for every tool, subagent included — no longer
// prematurely completes an async spawn's widget on its own immediate ack,
// and that the later real completion (pushed separately, keyed by job id)
// does.
func TestHandleEvent_SubagentAckThenRealCompletion(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	tui.mu.Lock()
	tui.handleEvent(agent.OutputEvent{
		Type:       agent.OutputToolStart,
		ToolName:   "subagent",
		ToolCallID: "call-1",
		ToolInput:  []byte(`{"task":"look around","name":"scout"}`),
	})
	tui.handleEvent(agent.OutputEvent{
		Type:              agent.OutputToolResult,
		ToolName:          "subagent",
		ToolCallID:        "call-1",
		ToolResultContent: `Subagent "scout" spawned as job sub-a1b2c3d4. It runs in the background — use subagent_status to check progress, subagent_result to retrieve the final output once done.`,
	})
	tui.mu.Unlock()

	tui.mu.Lock()
	done := tui.scroll.blocks[0].meta.ToolDone
	jobID := tui.scroll.blocks[0].meta.SubagentJobID
	tui.mu.Unlock()
	if done {
		t.Fatal("widget completed on the spawn ack alone — should still be running")
	}
	if jobID != "sub-a1b2c3d4" {
		t.Fatalf("SubagentJobID = %q, want sub-a1b2c3d4", jobID)
	}

	tui.mu.Lock()
	tui.handleEvent(agent.OutputEvent{
		Type:              agent.OutputToolResult,
		ToolName:          "subagent",
		ToolCallID:        "sub-a1b2c3d4", // keyed by job id, as Agent.CompleteSubagentJob sends
		ToolResultContent: "did the thing\n\n---\nSubagent finished. 1 tool calls, 1 turns. Ran on anthropic/claude-opus-5.",
	})
	done = tui.scroll.blocks[0].meta.ToolDone
	rows := layoutSubagentCard(&tui.scroll.blocks[0], 80)
	tui.mu.Unlock()
	if !done {
		t.Fatal("widget did not complete on the job-id-keyed real result")
	}
	if !strings.Contains(stripANSI(rows[0].Text), "claude-opus-5") {
		t.Fatalf("expected the authoritative ran-on label to render, got %q", stripANSI(rows[0].Text))
	}
}
