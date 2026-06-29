package agent

import (
	"encoding/json"
	"testing"
	"time"

	"poisson/internal/provider"
	"poisson/internal/store"
)

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

func TestCompactSummarizesAllMessages(t *testing.T) {
	s := newTestStore(t)
	sid := newTestSession(t, s, "test-model")
	now := time.Now().Unix()
	for i, role := range []string{"user", "assistant", "user", "assistant"} {
		content, _ := json.Marshal([]map[string]string{{"type": "text", "text": role + " msg"}})
		if err := s.AppendMessage(&store.Message{
			SessionID: sid, Role: role, Content: string(content), CreatedAt: now + int64(i),
		}); err != nil {
			t.Fatal(err)
		}
	}

	fp := newFakeProvider()
	fp.SetResponses([][]provider.StreamEvent{
		provider.FakeTextResponse("## Big Picture\nAll compacted", nil),
	})
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, make(chan OutputEvent, 8), func(_, _, _ string) bool { return true })

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