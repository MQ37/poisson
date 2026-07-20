package agent

import (
	"context"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/provider"
)

// drainBuffered reads whatever's already sitting in ch's buffer without
// blocking — safe here because the channel is created with ample buffer
// capacity (256) for these small single-turn tests and nothing else drains
// it concurrently, so everything Prompt pushed is already buffered by the
// time it returns. This sidesteps the pre-existing data race in the
// package's drainEvents helper (an unsynchronized goroutine + slice, never
// exercised by any existing caller because none of them read the result).
func drainBuffered(ch chan OutputEvent) []OutputEvent {
	var out []OutputEvent
	for {
		select {
		case ev := <-ch:
			out = append(out, ev)
		default:
			return out
		}
	}
}

func TestStreamWithRetryNoticeSendsOneStartAndOneRecoveredEvent(t *testing.T) {
	s := newTestStore(t)
	sessionID := newTestSession(t, s, "test-model")
	fp := &retryScriptProvider{
		retries: []retryScriptStep{
			{attempt: 1, delay: time.Second, reason: "dial tcp: connection refused"},
			{attempt: 2, delay: 2 * time.Second, reason: "dial tcp: connection refused"},
			{attempt: 3, delay: 4 * time.Second, reason: "dial tcp: connection refused"},
		},
		recovered: true,
		response:  provider.FakeTextResponse("hi", &provider.Usage{InputTokens: 10, OutputTokens: 2}),
	}
	ch := make(chan OutputEvent, 256)
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sessionID, ch, nil)

	if err := a.Prompt("hi"); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	events := drainBuffered(ch)

	var retryingEvents []OutputEvent
	for _, ev := range events {
		if ev.Type == OutputRetrying {
			retryingEvents = append(retryingEvents, ev)
		}
	}
	if len(retryingEvents) != 2 {
		t.Fatalf("OutputRetrying events = %d, want exactly 2 (one start, one recovered) across 3 retry attempts: %+v", len(retryingEvents), retryingEvents)
	}
	if want := "connection lost: dial tcp: connection refused — reconnecting…"; retryingEvents[0].Text != want {
		t.Errorf("start text = %q, want %q", retryingEvents[0].Text, want)
	}
	if want := "reconnected — resuming"; retryingEvents[1].Text != want {
		t.Errorf("recovered text = %q, want %q", retryingEvents[1].Text, want)
	}
}

func TestStreamWithRetryNoticeNoEventsWhenNoRetryHappens(t *testing.T) {
	s := newTestStore(t)
	sessionID := newTestSession(t, s, "test-model")
	fp := &retryScriptProvider{
		response: provider.FakeTextResponse("hi", &provider.Usage{InputTokens: 10, OutputTokens: 2}),
	}
	ch := make(chan OutputEvent, 256)
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sessionID, ch, nil)

	if err := a.Prompt("hi"); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	for _, ev := range drainBuffered(ch) {
		if ev.Type == OutputRetrying {
			t.Fatalf("unexpected OutputRetrying event with no retry: %+v", ev)
		}
	}
}

// blockingUntilCancelProvider simulates a Stream() call stuck in a network
// retry backoff sleep: it blocks until ctx is cancelled, then returns
// ctx.Err() — exactly DoWithRetry's shape when cancelled mid-backoff.
type blockingUntilCancelProvider struct{}

func (p *blockingUntilCancelProvider) ID() string                        { return "blocking" }
func (p *blockingUntilCancelProvider) Models() ([]provider.Model, error) { return nil, nil }
func (p *blockingUntilCancelProvider) Stream(ctx context.Context, req *provider.Request) (<-chan provider.StreamEvent, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestRunTurnCancelledDuringStreamIsSilentNotAnError(t *testing.T) {
	s := newTestStore(t)
	sessionID := newTestSession(t, s, "test-model")
	fp := &blockingUntilCancelProvider{}
	ch := make(chan OutputEvent, 256)
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sessionID, ch, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- a.PromptWithContext(ctx, "hi")
	}()

	time.Sleep(20 * time.Millisecond) // let it enter Stream() and block on ctx.Done()
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error (context cancelled) to propagate to the caller")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("PromptWithContext did not return promptly after cancellation")
	}

	for _, ev := range drainBuffered(ch) {
		if ev.Type == OutputError {
			t.Fatalf("cancellation during Stream() must be silent, got OutputError: %+v", ev)
		}
	}
}

// maxElapsedCapturingProvider records what MaxElapsed it sees on the trace
// attached to its ctx, to verify streamWithRetryNotice preserves a
// caller-supplied budget instead of dropping it when it builds its own
// trace for OutputRetrying notifications.
type maxElapsedCapturingProvider struct {
	seenMaxElapsed time.Duration
	response       []provider.StreamEvent
}

func (p *maxElapsedCapturingProvider) ID() string                        { return "capture" }
func (p *maxElapsedCapturingProvider) Models() ([]provider.Model, error) { return nil, nil }
func (p *maxElapsedCapturingProvider) Stream(ctx context.Context, req *provider.Request) (<-chan provider.StreamEvent, error) {
	if trace := provider.RetryTraceFromContext(ctx); trace != nil {
		p.seenMaxElapsed = trace.MaxElapsed
	}
	ch := make(chan provider.StreamEvent, len(p.response)+1)
	go func() {
		defer close(ch)
		for _, ev := range p.response {
			ch <- ev
		}
	}()
	return ch, nil
}

func TestStreamWithRetryNoticePreservesCallerMaxElapsed(t *testing.T) {
	s := newTestStore(t)
	sessionID := newTestSession(t, s, "test-model")
	fp := &maxElapsedCapturingProvider{
		response: provider.FakeTextResponse("hi", &provider.Usage{InputTokens: 10, OutputTokens: 2}),
	}
	ch := make(chan OutputEvent, 256)
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sessionID, ch, nil)

	budget := 3 * time.Minute
	ctx := provider.WithRetryTrace(context.Background(), &provider.RetryTrace{MaxElapsed: budget})
	if err := a.PromptWithContext(ctx, "hi"); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if fp.seenMaxElapsed != budget {
		t.Errorf("provider saw MaxElapsed = %v, want %v (streamWithRetryNotice must preserve the caller's budget)", fp.seenMaxElapsed, budget)
	}
}
