package agent

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/testutil"
)

// TestRunTurnRetriesRetryableMidStreamError verifies runTurn transparently
// retries a round that fails with a Retryable EventError (e.g. Anthropic's
// overloaded_error arriving after HTTP 200 — see provider.StreamEvent.Retryable)
// when nothing has streamed to the user yet, and that the retry is visible.
func TestRunTurnRetriesRetryableMidStreamError(t *testing.T) {
	old := midStreamErrorBackoff
	midStreamErrorBackoff = time.Millisecond
	defer func() { midStreamErrorBackoff = old }()

	s := newTestStore(t)
	sessionID := newTestSession(t, s, "test-model")
	reg := newTestRegistry(testutil.TempDir(t))
	cfg := newTestConfig()
	p := newFakeProvider()
	p.SetResponses([][]provider.StreamEvent{
		provider.FakeRetryableErrorResponse(errors.New("anthropic: overloaded_error: Overloaded")),
		provider.FakeTextResponse("recovered fine", nil),
	})

	ch := make(chan OutputEvent, 256)
	var events []OutputEvent
	done := make(chan struct{})
	go func() {
		for ev := range ch {
			events = append(events, ev)
		}
		close(done)
	}()
	agent := NewAgent(s, p, reg, cfg, sessionID, ch, nil)
	if err := agent.Prompt("hi"); err != nil {
		t.Fatalf("Prompt should recover via mid-stream retry: %v", err)
	}
	close(ch)
	<-done

	if p.CallCount() != 2 {
		t.Errorf("CallCount = %d, want 2 (retryable error + one retry)", p.CallCount())
	}
	sawRetryNotice := false
	for _, ev := range events {
		if ev.Type == OutputRetrying && strings.Contains(ev.Text, "provider overloaded") {
			sawRetryNotice = true
		}
	}
	if !sawRetryNotice {
		t.Errorf("expected a visible provider-overloaded retry notice, got %+v", events)
	}
	msgs, _ := s.GetMessages(sessionID)
	if len(msgs) < 2 || msgs[len(msgs)-1].Role != "assistant" {
		t.Fatalf("expected a final assistant message, got %d msgs", len(msgs))
	}
	if !strings.Contains(msgs[len(msgs)-1].Content, "recovered fine") {
		t.Errorf("assistant message missing retried content: %s", msgs[len(msgs)-1].Content)
	}
}

// TestRunTurnGivesUpAfterMidStreamErrorRetries verifies the retry budget is
// bounded: a Retryable error that never clears eventually fails the turn
// instead of retrying forever.
func TestRunTurnGivesUpAfterMidStreamErrorRetries(t *testing.T) {
	old := midStreamErrorBackoff
	midStreamErrorBackoff = time.Millisecond
	defer func() { midStreamErrorBackoff = old }()

	s := newTestStore(t)
	sessionID := newTestSession(t, s, "test-model")
	reg := newTestRegistry(testutil.TempDir(t))
	cfg := newTestConfig()
	p := newFakeProvider()
	always := provider.FakeRetryableErrorResponse(errors.New("anthropic: overloaded_error: Overloaded"))
	p.SetResponses([][]provider.StreamEvent{always, always, always, always, always})

	ch := make(chan OutputEvent, 256)
	drainEvents(ch)
	agent := NewAgent(s, p, reg, cfg, sessionID, ch, nil)
	if err := agent.Prompt("hi"); err == nil {
		t.Fatal("expected an error after exhausting mid-stream error retries")
	}
	if want := maxMidStreamErrorRetries + 1; p.CallCount() != want {
		t.Errorf("CallCount = %d, want %d (initial + %d retries)", p.CallCount(), want, maxMidStreamErrorRetries)
	}
}

// TestRunTurnDoesNotRetryNonRetryableMidStreamError verifies a non-Retryable
// error (e.g. a client-side mistake like an invalid request) fails the turn
// immediately, with no retry attempt — retrying it would waste a call on
// something backoff can never fix.
func TestRunTurnDoesNotRetryNonRetryableMidStreamError(t *testing.T) {
	s := newTestStore(t)
	sessionID := newTestSession(t, s, "test-model")
	reg := newTestRegistry(testutil.TempDir(t))
	cfg := newTestConfig()
	p := newFakeProvider()
	p.SetResponses([][]provider.StreamEvent{
		provider.FakeErrorResponse(errors.New("anthropic: invalid_request_error: bad input")),
	})

	ch := make(chan OutputEvent, 256)
	drainEvents(ch)
	agent := NewAgent(s, p, reg, cfg, sessionID, ch, nil)
	if err := agent.Prompt("hi"); err == nil {
		t.Fatal("expected an immediate error for a non-retryable mid-stream error")
	}
	if p.CallCount() != 1 {
		t.Errorf("CallCount = %d, want 1 (no retry for a non-retryable error)", p.CallCount())
	}
}

// TestRunTurnDoesNotRetryMidStreamErrorAfterContentAlreadyStreamed verifies
// that a Retryable error arriving after some text has already reached the
// user is NOT retried — retrying would resend the round from scratch and
// duplicate content the user already saw.
func TestRunTurnDoesNotRetryMidStreamErrorAfterContentAlreadyStreamed(t *testing.T) {
	old := midStreamErrorBackoff
	midStreamErrorBackoff = time.Millisecond
	defer func() { midStreamErrorBackoff = old }()

	s := newTestStore(t)
	sessionID := newTestSession(t, s, "test-model")
	reg := newTestRegistry(testutil.TempDir(t))
	cfg := newTestConfig()
	p := newFakeProvider()
	p.SetResponses([][]provider.StreamEvent{
		{
			{Type: provider.EventTextDelta, Text: "partial answer that already reached the user"},
			{Type: provider.EventError, Error: errors.New("anthropic: overloaded_error: Overloaded"), Retryable: true},
		},
	})

	ch := make(chan OutputEvent, 256)
	drainEvents(ch)
	agent := NewAgent(s, p, reg, cfg, sessionID, ch, nil)
	if err := agent.Prompt("hi"); err == nil {
		t.Fatal("expected an error: a mid-stream error after content already streamed must not retry")
	}
	if p.CallCount() != 1 {
		t.Errorf("CallCount = %d, want 1 (no retry once content has already streamed to the user)", p.CallCount())
	}
}
