package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"poisson/internal/provider"
	"poisson/internal/testutil"
)

// TestPendingInputFoldedIntoToolResult verifies a message queued while a tool
// round is running is folded into that round's last tool_result — NOT
// appended as its own user-role row, which would (once coalesced with the
// tool results into one "user" wire message) produce two consecutive
// user-role messages, which Anthropic rejects.
func TestPendingInputFoldedIntoToolResult(t *testing.T) {
	s := newTestStore(t)
	sessionID := newTestSession(t, s, "test-model")
	cwd := testutil.TempDir(t)
	testFile := filepath.Join(cwd, "hello.txt")
	if err := os.WriteFile(testFile, []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}
	reg := newTestRegistry(cwd)
	cfg := newTestConfig()
	p := newFakeProvider()
	first, second := provider.FakeToolCallResponse("read", map[string]interface{}{"path": testFile}, "done")
	p.SetResponses([][]provider.StreamEvent{first, second})

	ch := make(chan OutputEvent, 256)
	drainEvents(ch)
	a := NewAgent(s, p, reg, cfg, sessionID, ch, nil)

	consumed := false
	a.SetPendingInputFn(func() ([]TextSegment, bool) {
		if consumed {
			return nil, false
		}
		consumed = true
		return []TextSegment{{Text: "also check the other file"}}, true
	})

	if err := a.Prompt("read hello.txt"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	close(ch)

	if !consumed {
		t.Fatal("pendingInputFn should have been consumed")
	}

	msgs, err := s.GetMessages(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	var toolBlocks []contentBlockJSON
	if err := json.Unmarshal([]byte(msgs[2].Content), &toolBlocks); err != nil {
		t.Fatalf("parse tool content: %v", err)
	}
	got := false
	for _, b := range toolBlocks {
		if b.Type == "tool_result" && strings.Contains(b.ToolResult, "also check the other file") {
			got = true
		}
	}
	if !got {
		t.Errorf("queued message not folded into tool result: %s", msgs[2].Content)
	}
	for _, m := range msgs {
		if m.Role != "user" {
			continue
		}
		if strings.Contains(m.Content, "also check the other file") {
			t.Errorf("queued message must not become its own user-role row after a tool round: %+v", m)
		}
	}
}

// TestPendingInputAppendedAsNewTurnBeforeEnding verifies a message queued
// while the model is about to give its (tool-less) final answer is spliced
// in as a fresh user turn and the loop continues, instead of the turn ending
// and the message only being sent afterward.
func TestPendingInputAppendedAsNewTurnBeforeEnding(t *testing.T) {
	s := newTestStore(t)
	sessionID := newTestSession(t, s, "test-model")
	cfg := newTestConfig()
	p := newFakeProvider()
	p.SetResponses([][]provider.StreamEvent{
		provider.FakeTextResponse("first answer", nil),
		provider.FakeTextResponse("second answer", nil),
	})

	ch := make(chan OutputEvent, 256)
	drainEvents(ch)
	a := NewAgent(s, p, newTestRegistry("."), cfg, sessionID, ch, nil)

	consumed := false
	a.SetPendingInputFn(func() ([]TextSegment, bool) {
		if consumed {
			return nil, false
		}
		consumed = true
		return []TextSegment{{Text: "one more thing"}}, true
	})

	if err := a.Prompt("hello"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	close(ch)

	if !consumed {
		t.Fatal("pendingInputFn should have been consumed")
	}
	if p.CallCount() != 2 {
		t.Fatalf("CallCount = %d, want 2 (turn should have continued instead of ending)", p.CallCount())
	}

	msgs, err := s.GetMessages(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 4 {
		t.Fatalf("got %d messages, want 4 (user, assistant, injected user, assistant): %+v", len(msgs), msgs)
	}
	if msgs[1].Role != "assistant" || !strings.Contains(msgs[1].Content, "first answer") {
		t.Errorf("msgs[1] = %+v, want first answer", msgs[1])
	}
	if msgs[2].Role != "user" || !strings.Contains(msgs[2].Content, "one more thing") {
		t.Errorf("msgs[2] = %+v, want injected user turn", msgs[2])
	}
	if msgs[3].Role != "assistant" || !strings.Contains(msgs[3].Content, "second answer") {
		t.Errorf("msgs[3] = %+v, want second answer", msgs[3])
	}
}

// TestPendingInputNilFnNeverPolled verifies a nil pendingInputFn (the
// default — tests, headless/subagent use) is simply never checked, and
// existing turns behave exactly as before this feature existed.
func TestPendingInputNilFnNeverPolled(t *testing.T) {
	s := newTestStore(t)
	sessionID := newTestSession(t, s, "test-model")
	cfg := newTestConfig()
	p := newFakeProvider()
	p.SetResponses([][]provider.StreamEvent{provider.FakeTextResponse("only answer", nil)})

	ch := make(chan OutputEvent, 256)
	drainEvents(ch)
	a := NewAgent(s, p, newTestRegistry("."), cfg, sessionID, ch, nil)

	if err := a.Prompt("hello"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	close(ch)

	if p.CallCount() != 1 {
		t.Fatalf("CallCount = %d, want 1", p.CallCount())
	}
}
