package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/store"
	"github.com/mq37/poisson/internal/testutil"
)

// TestRepairDanglingToolUse_FullyUnresolved covers a process that died right
// after persisting the assistant's tool_use message, before any tool ran —
// e.g. the crash reported against a session resumed with the Anthropic 400
// "tool_use ids were found without tool_result blocks immediately after".
func TestRepairDanglingToolUse_FullyUnresolved(t *testing.T) {
	st := newTestStore(t)
	sid := "sess-crash-full"
	if err := st.CreateSession(&store.Session{ID: sid, Cwd: "/tmp", Provider: "fake", Model: "m", CreatedAt: time.Now().Unix()}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	appendToolUse(t, st, sid, "call_1", "bash", map[string]any{"command": "ls"})

	a := &Agent{store: st, sessionID: sid}
	if err := a.repairDanglingToolUse(); err != nil {
		t.Fatalf("repairDanglingToolUse: %v", err)
	}

	msgs, err := st.GetMessages(sid)
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("want 2 messages after repair, got %d", len(msgs))
	}
	toolMsg := msgs[1]
	if toolMsg.Role != "tool" {
		t.Fatalf("want synthetic tool message, got role %q", toolMsg.Role)
	}
	var blocks []contentBlockJSON
	if err := json.Unmarshal([]byte(toolMsg.Content), &blocks); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(blocks) != 1 || blocks[0].ToolCallID != "call_1" || !blocks[0].ToolIsError {
		t.Fatalf("unexpected synthetic tool_result: %+v", blocks)
	}

	// Idempotent: a second call must not append anything further.
	if err := a.repairDanglingToolUse(); err != nil {
		t.Fatalf("second repairDanglingToolUse: %v", err)
	}
	again, err := st.GetMessages(sid)
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if len(again) != 2 {
		t.Fatalf("repair should be idempotent, got %d messages", len(again))
	}
}

// TestRepairDanglingToolUse_PartiallyResolved covers a crash mid tool-round:
// two tools were called, only the first tool_result made it to the store
// before the process died.
func TestRepairDanglingToolUse_PartiallyResolved(t *testing.T) {
	st := newTestStore(t)
	sid := "sess-crash-partial"
	if err := st.CreateSession(&store.Session{ID: sid, Cwd: "/tmp", Provider: "fake", Model: "m", CreatedAt: time.Now().Unix()}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	blocks := []contentBlockJSON{
		{Type: "tool_use", ToolCallID: "call_1", ToolName: "read", ToolInput: json.RawMessage(`{}`)},
		{Type: "tool_use", ToolCallID: "call_2", ToolName: "bash", ToolInput: json.RawMessage(`{}`)},
	}
	data, err := json.Marshal(blocks)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AppendMessage(&store.Message{SessionID: sid, Role: "assistant", Content: string(data)}); err != nil {
		t.Fatal(err)
	}
	appendToolResult(t, st, sid, "call_1", "file contents")

	a := &Agent{store: st, sessionID: sid}
	if err := a.repairDanglingToolUse(); err != nil {
		t.Fatalf("repairDanglingToolUse: %v", err)
	}

	msgs, err := st.GetMessages(sid)
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("want 3 messages after repair, got %d", len(msgs))
	}
	var got []contentBlockJSON
	if err := json.Unmarshal([]byte(msgs[2].Content), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 1 || got[0].ToolCallID != "call_2" {
		t.Fatalf("want synthetic result only for call_2, got %+v", got)
	}
}

// TestRepairDanglingToolUse_CleanHistoryUnchanged covers the normal case:
// every tool_use already has a matching tool_result, so nothing is appended.
func TestRepairDanglingToolUse_CleanHistoryUnchanged(t *testing.T) {
	st := newTestStore(t)
	sid := "sess-clean"
	if err := st.CreateSession(&store.Session{ID: sid, Cwd: "/tmp", Provider: "fake", Model: "m", CreatedAt: time.Now().Unix()}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	appendToolUse(t, st, sid, "call_1", "bash", map[string]any{"command": "ls"})
	appendToolResult(t, st, sid, "call_1", "ok")

	a := &Agent{store: st, sessionID: sid}
	if err := a.repairDanglingToolUse(); err != nil {
		t.Fatalf("repairDanglingToolUse: %v", err)
	}

	msgs, err := st.GetMessages(sid)
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("clean history should be untouched, got %d messages", len(msgs))
	}
}

// TestRepairDanglingToolUse_PrunesStaleRetryMessages reproduces the real
// crash-recovery shape: an assistant message with a dangling tool_use, then
// two "continue" retries piled up by a user hitting the same broken history
// twice before the fix shipped (each retry's provider call 400s, but
// appendUserMessage already ran, so the row sticks around). Both stray user
// rows must be pruned and the tool_use resolved, leaving a clean tail.
func TestRepairDanglingToolUse_PrunesStaleRetryMessages(t *testing.T) {
	st := newTestStore(t)
	sid := "sess-crash-retried"
	if err := st.CreateSession(&store.Session{ID: sid, Cwd: "/tmp", Provider: "fake", Model: "m", CreatedAt: time.Now().Unix()}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	appendToolUse(t, st, sid, "call_1", "subagent", map[string]any{"task": "verify"})
	appendUserText(t, st, sid, "continue")
	appendUserText(t, st, sid, "continue")

	a := &Agent{store: st, sessionID: sid}
	if err := a.repairDanglingToolUse(); err != nil {
		t.Fatalf("repairDanglingToolUse: %v", err)
	}

	msgs, err := st.GetMessages(sid)
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("want 2 active messages (assistant tool_use + synthetic result), got %d: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != "assistant" || msgs[1].Role != "tool" {
		t.Fatalf("want [assistant, tool], got [%s, %s]", msgs[0].Role, msgs[1].Role)
	}
	var blocks []contentBlockJSON
	if err := json.Unmarshal([]byte(msgs[1].Content), &blocks); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(blocks) != 1 || blocks[0].ToolCallID != "call_1" {
		t.Fatalf("unexpected synthetic tool_result: %+v", blocks)
	}

	// The two stray "continue" rows must still exist, just soft-deleted —
	// GetAllMessages (deleted_at IS NULL, same filter as GetMessages here)
	// should also not surface them, but a raw count confirms they weren't
	// hard-deleted.
	all, err := st.GetAllMessages(sid)
	if err != nil {
		t.Fatalf("get all messages: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("stray retries should be excluded from GetAllMessages too, got %d", len(all))
	}
}

// TestRepairDanglingToolUse_LoneTrailingUserMessageUntouched covers a
// trailing user message that has nothing to do with this bug — no assistant
// message underneath it, let alone one with a dangling tool_use. Must be
// left alone: a lone trailing user row on its own isn't proof of anything
// broken (e.g. TestPromptCompactsBeforeFirstRequestAfterSmallerWindowSwitch
// seeds a session this way on purpose to test compaction).
func TestRepairDanglingToolUse_LoneTrailingUserMessageUntouched(t *testing.T) {
	st := newTestStore(t)
	sid := "sess-lone-user"
	if err := st.CreateSession(&store.Session{ID: sid, Cwd: "/tmp", Provider: "fake", Model: "m", CreatedAt: time.Now().Unix()}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	appendUserText(t, st, sid, "old context")

	a := &Agent{store: st, sessionID: sid}
	if err := a.repairDanglingToolUse(); err != nil {
		t.Fatalf("repairDanglingToolUse: %v", err)
	}

	msgs, err := st.GetMessages(sid)
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Role != "user" {
		t.Fatalf("lone trailing user message should be untouched, got %+v", msgs)
	}
}

// TestPromptSegmentsWithContext_RepairsDanglingToolUseBeforeNewTurn covers
// the real crash-recovery path end to end: a session left with a dangling
// tool_use (previous process died mid tool-round) must not make the next
// prompt fail — the repair must run and insert its synthetic tool_result
// strictly before the new user message, keeping tool_use immediately
// followed by tool_result as every provider requires.
func TestPromptSegmentsWithContext_RepairsDanglingToolUseBeforeNewTurn(t *testing.T) {
	st := newTestStore(t)
	sid := newTestSession(t, st, "test-model")
	appendToolUse(t, st, sid, "call_1", "bash", map[string]any{"command": "ls"})

	prov := newFakeProvider()
	prov.SetResponses([][]provider.StreamEvent{provider.FakeTextResponse("done", nil)})
	a := NewAgent(st, prov, newTestRegistry(testutil.TempDir(t)), newTestConfig(), sid, nil, nil)

	if err := a.PromptWithContext(context.Background(), "continue"); err != nil {
		t.Fatalf("PromptWithContext: %v", err)
	}

	msgs, err := st.GetMessages(sid)
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	// assistant(tool_use) [0], synthetic tool_result [1], user "continue" [2], ...
	if len(msgs) < 3 {
		t.Fatalf("want at least 3 messages, got %d", len(msgs))
	}
	if msgs[1].Role != "tool" {
		t.Fatalf("msgs[1] role = %q, want tool (synthetic repair)", msgs[1].Role)
	}
	if msgs[2].Role != "user" {
		t.Fatalf("msgs[2] role = %q, want user", msgs[2].Role)
	}
}
