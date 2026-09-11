package tui

import (
	"testing"

	"github.com/mq37/poisson/internal/agent"
)

// TestHandleEvent_SubagentCompletionBeforeAck is the regression guard for the
// ack/completion ordering race (see agent.OutputSubagentJobResult's doc
// comment): the ack (sent by the turn-dispatch goroutine) and the real
// completion (sent later by the background job goroutine via
// Agent.CompleteSubagentJob) race to land on the same channel with no
// ordering guarantee. Before OutputSubagentJobResult was keyed by the
// spawning tool-call id, a completion arriving first found no matching
// widget (it only knew the job id, which the widget only learns from the
// ack) and was silently dropped — leaving the widget stuck "running"
// forever once the ack arrived after and correctly declined to complete it.
// Keying by tool-call id instead removes the ordering dependency entirely:
// this test proves it by delivering the events in the "wrong" order.
func TestHandleEvent_SubagentCompletionBeforeAck(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	tui.mu.Lock()
	tui.handleEvent(agent.OutputEvent{
		Type:       agent.OutputToolStart,
		ToolName:   "subagent",
		ToolCallID: "call-1",
		ToolInput:  []byte(`{"task":"look around","name":"scout"}`),
	})
	// Completion arrives BEFORE the ack — the race this test exists for.
	tui.handleEvent(agent.OutputEvent{
		Type:              agent.OutputSubagentJobResult,
		ToolName:          "subagent",
		ToolCallID:        "call-1",
		ToolResultContent: "did the thing\n\n---\nSubagent finished. 1 tool calls, 1 turns. Ran on anthropic/claude-opus-5.",
	})
	doneAfterCompletion := tui.scroll.blocks[0].meta.ToolDone
	// The ack, arriving late, must not un-finish or otherwise disturb it.
	tui.handleEvent(agent.OutputEvent{
		Type:              agent.OutputToolResult,
		ToolName:          "subagent",
		ToolCallID:        "call-1",
		ToolResultContent: `Subagent "scout" spawned as job sub-a1b2c3d4. It runs in the background — use subagent_status to check progress, subagent_result to retrieve the final output once done.`,
	})
	doneAfterLateAck := tui.scroll.blocks[0].meta.ToolDone
	tui.mu.Unlock()

	if !doneAfterCompletion {
		t.Fatal("widget not completed by the out-of-order completion — stuck \"running\" forever (the bug this test guards against)")
	}
	if !doneAfterLateAck {
		t.Fatal("late-arriving ack un-finished an already-completed widget")
	}
}

// TestActiveToolsNotDoubleDecrementedBySubagentCompletion is the regression
// guard for markAfterEvent double-decrementing activeTools: a subagent call
// produces one OutputToolStart but two tool-result-shaped events (the ack,
// still an OutputToolResult, and the real completion, now a distinct
// OutputSubagentJobResult) — only the ack may decrement.
func TestActiveToolsNotDoubleDecrementedBySubagentCompletion(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	tui.mu.Lock()
	tui.handleEvent(agent.OutputEvent{Type: agent.OutputToolStart, ToolName: "subagent", ToolCallID: "call-1"})
	tui.markAfterEvent(agent.OutputEvent{Type: agent.OutputToolStart, ToolName: "subagent", ToolCallID: "call-1"})
	if tui.activeTools != 1 {
		t.Fatalf("activeTools after start = %d, want 1", tui.activeTools)
	}

	tui.handleEvent(agent.OutputEvent{
		Type: agent.OutputToolResult, ToolName: "subagent", ToolCallID: "call-1",
		ToolResultContent: `Subagent "scout" spawned as job sub-x. It runs in the background — use subagent_status to check progress, subagent_result to retrieve the final output once done.`,
	})
	tui.markAfterEvent(agent.OutputEvent{Type: agent.OutputToolResult, ToolName: "subagent", ToolCallID: "call-1"})
	if tui.activeTools != 0 {
		t.Fatalf("activeTools after ack = %d, want 0", tui.activeTools)
	}

	// A second, unrelated tool starts before the subagent job's real
	// completion arrives — the double-decrement bug stole this one's count.
	tui.markAfterEvent(agent.OutputEvent{Type: agent.OutputToolStart, ToolName: "bash", ToolCallID: "call-2"})
	if tui.activeTools != 1 {
		t.Fatalf("activeTools after unrelated bash start = %d, want 1", tui.activeTools)
	}

	tui.markAfterEvent(agent.OutputEvent{Type: agent.OutputSubagentJobResult, ToolName: "subagent", ToolCallID: "call-1"})
	if tui.activeTools != 1 {
		t.Fatalf("activeTools after subagent's real completion = %d, want 1 (must not steal the still-running bash call's count)", tui.activeTools)
	}
}

// TestUpdateSubagentProgressIgnoresFinishedCard is the regression guard for
// bug 12: a late progress/retry tick landing after completion must not
// resurrect SubagentStatus on an already-done card (layoutSubagentCard's
// reconnecting check would then wrongly suppress its turn/context/model
// display).
func TestUpdateSubagentProgressIgnoresFinishedCard(t *testing.T) {
	s := newScrollback(1024)
	s.appendSubagentCard(1, "call-1", "scout", "look around", "anthropic/claude-opus-5")
	if !s.completeSubagentCard("call-1", "Subagent finished. 1 tool calls, 1 turns.", "", 500) {
		t.Fatal("completeSubagentCard did not match")
	}

	s.updateSubagentProgress("call-1", 5, 1000, 8192, 12.5, "connection lost: timeout — reconnecting…")

	if s.blocks[0].meta.SubagentStatus != "" {
		t.Fatalf("SubagentStatus = %q, want unchanged (empty) on a finished card", s.blocks[0].meta.SubagentStatus)
	}
	if !s.blocks[0].meta.ToolDone {
		t.Fatal("card must remain done")
	}
}
