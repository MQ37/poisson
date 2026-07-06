package agent

import (
	"context"
	"encoding/json"
	"errors"
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
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, make(chan OutputEvent, 8), func(context.Context, string, string, string) bool { return true })
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

func TestCompactSummarizesAllMessages(t *testing.T) {
	s := newTestStore(t)
	sid := newTestSession(t, s, "test-model")
	seedMessages(t, s, sid, []string{"user", "assistant", "user", "assistant"})

	fp := newFakeProvider()
	fp.SetResponses([][]provider.StreamEvent{
		provider.FakeTextResponse("## Big Picture\nAll compacted", nil),
	})
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, make(chan OutputEvent, 8), func(context.Context, string, string, string) bool { return true })

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
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, make(chan OutputEvent, 8), func(context.Context, string, string, string) bool { return true })

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
func TestCompactAutoNothingWhenSingleTurn(t *testing.T) {
	s := newTestStore(t)
	sid := newTestSession(t, s, "test-model")
	seedMessages(t, s, sid, []string{"user", "assistant", "tool"})

	fp := newFakeProvider()
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, make(chan OutputEvent, 8), func(context.Context, string, string, string) bool { return true })

	if err := a.compact(context.Background(), false, true); !errors.Is(err, ErrNothingToCompact) {
		t.Fatalf("compact = %v, want ErrNothingToCompact", err)
	}
	msgs, err := s.GetMessages(sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 {
		t.Fatalf("active messages = %d, want 3 (unchanged)", len(msgs))
	}
}