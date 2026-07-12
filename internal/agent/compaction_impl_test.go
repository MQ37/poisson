package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"poisson/internal/provider"
	"poisson/internal/store"
)

// seedMessages appends messages with the given roles to a session.
func seedMessages(t *testing.T, s *store.Store, sid string, roles []string) {
	t.Helper()
	now := time.Now().Unix()
	for i, role := range roles {
		content, _ := json.Marshal([]map[string]string{{"type": "text", "text": role + " msg"}})
		if err := s.AppendMessage(&store.Message{
			SessionID: sid, Role: role, Content: string(content), CreatedAt: now + int64(i),
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAdjustCompactionCount_IncludesTrailingTools(t *testing.T) {
	msgs := []store.Message{
		{Seq: 1, Role: "user"},
		{Seq: 2, Role: "assistant"},
		{Seq: 3, Role: "tool"},
		{Seq: 4, Role: "tool"},
		{Seq: 5, Role: "assistant"},
	}
	got := adjustCompactionCount(msgs, 2)
	if got != 4 {
		t.Fatalf("adjustCompactionCount = %d, want 4 (include tool results)", got)
	}
}

func TestAdjustCompactionCount_NoOrphanTools(t *testing.T) {
	msgs := []store.Message{
		{Seq: 1, Role: "user"},
		{Seq: 2, Role: "assistant"},
		{Seq: 3, Role: "user"},
	}
	got := adjustCompactionCount(msgs, 2)
	if got != 2 {
		t.Fatalf("adjustCompactionCount = %d, want 2", got)
	}
}

// TestCompactManualKeepsUserFirst guards against a leading assistant/tool
// message in the kept tail after a budget-limited manual /compact. The summary
// is a separate system block, so Messages must begin with a user turn or the
// provider (Anthropic) rejects the next request.
func TestCompactManualKeepsUserFirst(t *testing.T) {
	s := newTestStore(t)
	sid := newTestSession(t, s, "test-model")
	big := strings.Repeat("x ", 2400) // ~960 tokens/msg so 6 exceed the 0.65*8192 budget but 3 don't
	roles := []string{"user", "assistant", "user", "assistant", "user", "assistant"}
	now := time.Now().Unix()
	for i, role := range roles {
		content, _ := json.Marshal([]map[string]string{{"type": "text", "text": role + " " + big}})
		if err := s.AppendMessage(&store.Message{
			SessionID: sid, Role: role, Content: string(content), CreatedAt: now + int64(i),
		}); err != nil {
			t.Fatal(err)
		}
	}
	fp := newFakeProvider()
	fp.SetResponses([][]provider.StreamEvent{provider.FakeTextResponse("summary", nil)})
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, make(chan OutputEvent, 8), func(context.Context, string, string, string) (bool, string) { return true, "" })
	if err := a.Compact(); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	msgs, err := s.GetMessages(sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) > 0 && msgs[0].Role != "user" {
		t.Fatalf("kept tail starts with %q, want user (valid request start); kept %d of %d", msgs[0].Role, len(msgs), len(roles))
	}
}

// TestCompactionRequestAlwaysEndsWithUserMessage reproduces a real 400 from
// Anthropic's newer models: "This model does not support assistant message
// prefill. The conversation must end with a user message." toSummarize (the
// messages being fed to the summarizer, as opposed to the kept tail) ends
// wherever the boundary search happens to land — usually an assistant or tool
// message, since the search only guarantees the KEPT tail starts with "user",
// not that the summarized transcript ENDS with one. The summarization request
// is a separate provider.Request built from scratch, so it must independently
// satisfy the same "ends with user" rule any Anthropic request does.
func TestCompactionRequestAlwaysEndsWithUserMessage(t *testing.T) {
	s := newTestStore(t)
	sid := newTestSession(t, s, "test-model")
	// assistant, tool, assistant: the boundary search stops at the next "user"
	// (there isn't one), so summarizeCount == len(msgs) and toSummarize's last
	// message is the trailing assistant reply — exactly what triggers the 400.
	seedMessages(t, s, sid, []string{"user", "assistant", "tool", "assistant"})

	fp := newFakeProvider()
	fp.SetResponses([][]provider.StreamEvent{
		provider.FakeTextResponse("## Big Picture\nsummary", nil),
	})
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, make(chan OutputEvent, 8), func(context.Context, string, string, string) (bool, string) { return true, "" })

	if err := a.Compact(); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	req := fp.LastRequest()
	if req == nil || len(req.Messages) == 0 {
		t.Fatal("expected a captured compaction request")
	}
	if last := req.Messages[len(req.Messages)-1]; last.Role != "user" {
		t.Fatalf("compaction request's last message role = %q, want %q (Anthropic rejects a request ending in assistant role as unsupported message prefill)", last.Role, "user")
	}
}

func TestCompactSummarizesAllMessages(t *testing.T) {
	s := newTestStore(t)
	sid := newTestSession(t, s, "test-model")
	seedMessages(t, s, sid, []string{"user", "assistant", "user", "assistant"})

	fp := newFakeProvider()
	fp.SetResponses([][]provider.StreamEvent{
		provider.FakeTextResponse("## Big Picture\nAll compacted", nil),
	})
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, make(chan OutputEvent, 8), func(context.Context, string, string, string) (bool, string) { return true, "" })

	if err := a.Compact(); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	msgs, err := s.GetMessages(sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Fatalf("active messages = %d, want 0 (whole conversation compacted)", len(msgs))
	}
}

// TestCompactRecordsSummaryTokensNotZero reproduces a reported UX bug: after
// a manual /compact that summarizes the whole conversation (nothing left in
// the active set), the notice showed "191,321 -> 0 tokens" — a nonsensical
// after-count, since the compaction summary itself (what's ACTUALLY carried
// forward into every future request, per agent.go's system-block injection)
// costs real tokens. The old code summed only the active messages' tokens,
// ignoring the summary entirely whenever it emptied the active set to zero.
func TestCompactRecordsSummaryTokensNotZero(t *testing.T) {
	s := newTestStore(t)
	sid := newTestSession(t, s, "test-model")
	seedMessages(t, s, sid, []string{"user", "assistant", "user", "assistant"})

	summary := "## Big Picture\n" + strings.Repeat("detail ", 200) // long enough for a non-trivial token estimate
	fp := newFakeProvider()
	fp.SetResponses([][]provider.StreamEvent{provider.FakeTextResponse(summary, nil)})
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, make(chan OutputEvent, 8), func(context.Context, string, string, string) (bool, string) { return true, "" })

	if err := a.Compact(); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	msgs, err := s.GetMessages(sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Fatalf("active messages = %d, want 0 (precondition: whole conversation compacted)", len(msgs))
	}

	want := a.EstimateTokens(summary)
	comp, err := s.GetLastCompaction(sid)
	if err != nil || comp == nil {
		t.Fatalf("GetLastCompaction: %v, %+v", err, comp)
	}
	if comp.TokensAfter != want {
		t.Fatalf("TokensAfter = %d, want %d (the summary's own token cost, not 0)", comp.TokensAfter, want)
	}
}

// TestCompactAutoKeepsActiveTail verifies auto-compaction never empties the
// active set: it summarizes everything before the current turn and keeps that
// turn (starting with a user message) active, so the run loop's next request is
// valid and non-empty.
func TestCompactAutoKeepsActiveTail(t *testing.T) {
	s := newTestStore(t)
	sid := newTestSession(t, s, "test-model")
	seedMessages(t, s, sid, []string{"user", "assistant", "user", "assistant", "tool"})

	fp := newFakeProvider()
	fp.SetResponses([][]provider.StreamEvent{
		provider.FakeTextResponse("## Big Picture\nolder turns", nil),
	})
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, make(chan OutputEvent, 8), func(context.Context, string, string, string) (bool, string) { return true, "" })

	if err := a.compact(context.Background(), false, true); err != nil {
		t.Fatalf("compact: %v", err)
	}
	msgs, err := s.GetMessages(sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 {
		t.Fatalf("active messages = %d, want 3 (current turn kept)", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Fatalf("first active message role = %q, want user (valid request start)", msgs[0].Role)
	}
}

// TestCompactAutoNothingWhenSingleTurn verifies auto-compaction bails without
// touching messages when only the current turn exists (nothing older to
// summarize), rather than emptying the active set.
// TestCompactAutoAppendsContinuationOnSingleTurn covers the normal subagent
// trajectory: one task, then an extended tool-calling run with no further
// "user" message to split at. Auto-compaction must still be able to trigger
// (summarize everything) instead of silently doing nothing forever — it
// appends a synthetic user turn so the next request still opens with a user
// message.
func TestCompactAutoAppendsContinuationOnSingleTurn(t *testing.T) {
	s := newTestStore(t)
	sid := newTestSession(t, s, "test-model")
	seedMessages(t, s, sid, []string{"user", "assistant", "tool"})

	fp := newFakeProvider()
	fp.SetResponses([][]provider.StreamEvent{provider.FakeTextResponse("## Big Picture\nDid stuff.", nil)})
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, make(chan OutputEvent, 8), func(context.Context, string, string, string) (bool, string) { return true, "" })

	if err := a.compact(context.Background(), false, true); err != nil {
		t.Fatalf("compact = %v, want success", err)
	}
	sess, err := s.GetSession(sid)
	if err != nil || sess == nil || sess.CompactionSummary == nil || *sess.CompactionSummary == "" {
		t.Fatal("expected a compaction summary to be stored")
	}
	msgs, err := s.GetMessages(sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Role != "user" {
		t.Fatalf("active messages = %+v, want exactly one synthetic user continuation", msgs)
	}
}
