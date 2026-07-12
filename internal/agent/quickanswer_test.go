package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"poisson/internal/provider"
	"poisson/internal/store"
)

func TestStreamQuickAnswer(t *testing.T) {
	fp := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	fp.SetResponses([][]provider.StreamEvent{{
		{Type: provider.EventTextDelta, Text: "hello "},
		{Type: provider.EventTextDelta, Text: "world"},
		{Type: provider.EventDone, Usage: &provider.Usage{OutputTokens: 2}},
	}})

	s := newTestStore(t)
	sid := newTestSession(t, s, "m")
	ch := make(chan OutputEvent, 8)
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, ch, nil)
	a.SetModel("m")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	textCh, errCh, err := a.StreamQuickAnswer(ctx, "what is 2+2?")
	if err != nil {
		t.Fatalf("StreamQuickAnswer: %v", err)
	}
	var got string
	for chunk := range textCh {
		got += chunk
	}
	var streamErr error
	select {
	case streamErr = <-errCh:
	default:
	}
	if streamErr != nil {
		t.Fatalf("stream error: %v", streamErr)
	}
	if got != "hello world" {
		t.Fatalf("answer = %q, want %q", got, "hello world")
	}
	req := fp.LastRequest()
	if req == nil || len(req.Messages) != 1 {
		t.Fatalf("expected single user message, got %+v", req)
	}
	if len(ch) != 0 {
		t.Fatalf("outputChan should stay empty, got %d events", len(ch))
	}
}

func TestStreamQuickAnswerUsesConversationContext(t *testing.T) {
	fp := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	fp.SetResponses([][]provider.StreamEvent{{
		{Type: provider.EventTextDelta, Text: "ctx answer"},
		{Type: provider.EventDone, Usage: &provider.Usage{OutputTokens: 2}},
	}})
	s := newTestStore(t)
	sid := newTestSession(t, s, "m")
	// Seed a small conversation.
	uc, _ := contentBlocksToJSON([]provider.ContentBlock{{Type: "text", Text: "refactor the parser"}})
	ac, _ := contentBlocksToJSON([]provider.ContentBlock{{Type: "text", Text: "done, split into lexer + parser"}})
	if err := s.AppendMessage(&store.Message{SessionID: sid, Role: "user", Content: uc}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage(&store.Message{SessionID: sid, Role: "assistant", Content: ac}); err != nil {
		t.Fatal(err)
	}

	ch := make(chan OutputEvent, 8)
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, ch, nil)
	a.SetModel("m")

	textCh, _, err := a.StreamQuickAnswer(context.Background(), "which files did you change?")
	if err != nil {
		t.Fatalf("StreamQuickAnswer: %v", err)
	}
	for range textCh {
	}

	req := fp.LastRequest()
	if req == nil {
		t.Fatal("no request captured")
	}
	// History (2) + the appended question (1).
	if len(req.Messages) != 3 {
		t.Fatalf("messages = %d, want 3 (2 history + question)", len(req.Messages))
	}
	if req.Messages[0].Content[0].Text != "refactor the parser" {
		t.Errorf("conversation history not carried: %+v", req.Messages[0])
	}
	last := req.Messages[len(req.Messages)-1]
	if last.Role != "user" || !strings.Contains(last.Content[0].Text, "which files did you change?") {
		t.Errorf("question not appended as the final user turn: %+v", last)
	}
	// Same cache key + tools as the main request => reuses the prompt cache.
	if req.CacheKey != sid {
		t.Errorf("CacheKey = %q, want %q", req.CacheKey, sid)
	}
	if len(req.Tools) == 0 {
		t.Error("expected tools in the request so the cached prefix matches the main turn")
	}
}

func TestStreamQuickAnswerCancel(t *testing.T) {
	fp := provider.NewFakeProvider("fake", nil)
	fp.SetResponses([][]provider.StreamEvent{{
		{Type: provider.EventTextDelta, Text: "slow"},
	}})

	s := newTestStore(t)
	sid := newTestSession(t, s, "m")
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, nil, nil)
	a.SetModel("m")

	ctx, cancel := context.WithCancel(context.Background())
	textCh, errCh, err := a.StreamQuickAnswer(ctx, "wait")
	if err != nil {
		t.Fatalf("StreamQuickAnswer: %v", err)
	}
	cancel()
	for range textCh {
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("unexpected error after cancel: %v", err)
		}
	case <-time.After(time.Second):
	}
}
