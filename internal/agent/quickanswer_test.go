package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/store"
	"github.com/mq37/poisson/internal/testutil"
	"github.com/mq37/poisson/internal/tools"
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

	textCh, errCh, err := a.StreamQuickAnswer(ctx, "what is 2+2?", nil)
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
	var purpose string
	if err := s.DB().QueryRow(`SELECT purpose FROM api_calls WHERE session_id = ?`, sid).Scan(&purpose); err != nil {
		t.Fatalf("stored /btw call: %v", err)
	}
	if purpose != "btw" {
		t.Fatalf("purpose = %q, want btw", purpose)
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

	textCh, _, err := a.StreamQuickAnswer(context.Background(), "which files did you change?", nil)
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
	textCh, errCh, err := a.StreamQuickAnswer(ctx, "wait", nil)
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

// TestStreamQuickAnswerRunsReadOnlyTool confirms /btw can call an allowed
// read-only tool to ground its answer, and that the real tool result (not a
// denial) is what gets sent back to the model.
func TestStreamQuickAnswerRunsReadOnlyTool(t *testing.T) {
	dir := testutil.TempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("the secret number is 42"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry()
	reg.Register(tools.NewReadTool(dir, false, nil))

	fp := provider.NewFakeProvider("fake", nil)
	first, second := provider.FakeToolCallResponse("read", map[string]string{"path": "note.txt"}, "the answer is 42")
	fp.SetResponses([][]provider.StreamEvent{first, second})

	s := newTestStore(t)
	sid := newTestSession(t, s, "m")
	a := NewAgent(s, fp, reg, newTestConfig(), sid, nil, nil)
	a.SetModel("m")

	var statuses []string
	textCh, errCh, err := a.StreamQuickAnswer(context.Background(), "what's the secret number?", func(s string) {
		statuses = append(statuses, s)
	})
	if err != nil {
		t.Fatalf("StreamQuickAnswer: %v", err)
	}
	var got string
	for chunk := range textCh {
		got += chunk
	}
	if err := <-errCh; err != nil {
		t.Fatalf("stream error: %v", err)
	}
	// FakeToolCallResponse's tool-call round also emits leading text ("Let me
	// check that.") before the tool call, exactly like a real model narrating
	// before it calls a tool — that streams to textCh too, same as the main
	// agent surfaces it, so it's part of the answer.
	want := "Let me check that.the answer is 42"
	if got != want {
		t.Fatalf("answer = %q, want %q", got, want)
	}
	if len(statuses) != 1 || statuses[0] != "read(note.txt)" {
		t.Errorf("tool status callbacks = %v, want [\"read(note.txt)\"]", statuses)
	}

	reqs := fp.Requests()
	if len(reqs) != 2 {
		t.Fatalf("expected 2 requests (tool round + final), got %d", len(reqs))
	}
	secondReq := reqs[1]
	var sawResult bool
	for _, m := range secondReq.Messages {
		if m.Role != "tool" {
			continue
		}
		for _, b := range m.Content {
			if b.Type == "tool_result" {
				sawResult = true
				if b.ToolIsError {
					t.Errorf("tool result marked as error: %q", b.ToolResult)
				}
				if !strings.Contains(b.ToolResult, "the secret number is 42") {
					t.Errorf("tool result = %q, want real file content", b.ToolResult)
				}
			}
		}
	}
	if !sawResult {
		t.Fatal("no tool_result block found in the follow-up request")
	}
}

// TestStreamQuickAnswerDeniesDisallowedTool confirms a tool outside the
// read-only allowlist (bash here) is never executed — the model gets a
// denial tool_result instead, and the underlying tool's Execute never runs
// (proven by the sandboxed bash command's side effect never happening).
func TestStreamQuickAnswerDeniesDisallowedTool(t *testing.T) {
	dir := testutil.TempDir(t)
	marker := filepath.Join(dir, "marker")
	reg := tools.NewRegistry()
	// approvalFn always denies too, as a second line of defense, but the
	// point of this test is that Execute must never even be reached.
	reg.Register(tools.NewBashTool(dir, false, func(context.Context, string, string, string) (bool, string) { return true, "" }))

	fp := provider.NewFakeProvider("fake", nil)
	first, second := provider.FakeToolCallResponse("bash", map[string]string{"command": "touch " + marker, "description": "touch a marker file"}, "done")
	fp.SetResponses([][]provider.StreamEvent{first, second})

	s := newTestStore(t)
	sid := newTestSession(t, s, "m")
	a := NewAgent(s, fp, reg, newTestConfig(), sid, nil, nil)
	a.SetModel("m")

	textCh, errCh, err := a.StreamQuickAnswer(context.Background(), "run a command for me", nil)
	if err != nil {
		t.Fatalf("StreamQuickAnswer: %v", err)
	}
	for range textCh {
	}
	if err := <-errCh; err != nil {
		t.Fatalf("stream error: %v", err)
	}

	if _, err := os.Stat(marker); err == nil {
		t.Fatal("bash tool actually ran — marker file was created")
	}

	reqs := fp.Requests()
	if len(reqs) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(reqs))
	}
	var sawDenial bool
	for _, m := range reqs[1].Messages {
		if m.Role != "tool" {
			continue
		}
		for _, b := range m.Content {
			if b.Type == "tool_result" && b.ToolIsError && strings.Contains(b.ToolResult, "not available") {
				sawDenial = true
			}
		}
	}
	if !sawDenial {
		t.Fatal("expected a denial tool_result for the disallowed bash call")
	}
}

// TestStreamQuickAnswerCapsToolRounds confirms a model that keeps calling
// tools forever is cut off after btwMaxToolRounds, rather than looping
// (and re-streaming) indefinitely.
func TestStreamQuickAnswerCapsToolRounds(t *testing.T) {
	dir := testutil.TempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry()
	reg.Register(tools.NewReadTool(dir, false, nil))

	fp := provider.NewFakeProvider("fake", nil)
	// Script far more tool-call rounds than the cap allows; every response
	// keeps calling the tool again, never producing a final text-only answer.
	var responses [][]provider.StreamEvent
	for i := 0; i < btwMaxToolRounds+5; i++ {
		first, _ := provider.FakeToolCallResponse("read", map[string]string{"path": "note.txt"}, "unused")
		responses = append(responses, first)
	}
	fp.SetResponses(responses)

	s := newTestStore(t)
	sid := newTestSession(t, s, "m")
	a := NewAgent(s, fp, reg, newTestConfig(), sid, nil, nil)
	a.SetModel("m")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	textCh, errCh, err := a.StreamQuickAnswer(ctx, "keep looking", nil)
	if err != nil {
		t.Fatalf("StreamQuickAnswer: %v", err)
	}
	for range textCh {
	}
	if err := <-errCh; err != nil {
		t.Fatalf("stream error: %v", err)
	}

	// One request per round, capped at btwMaxToolRounds+1 (the extra +1 is the
	// final round where the loop sees round >= cap and stops without another
	// tool execution — but it still had to stream that round's response).
	if got := fp.CallCount(); got > btwMaxToolRounds+1 {
		t.Fatalf("provider called %d times, want at most %d (cap enforced)", got, btwMaxToolRounds+1)
	}
}
