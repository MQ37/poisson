package agent

import (
	"testing"
	"time"

	"github.com/mq37/poisson/internal/provider"
)

func newTestAgentForSpeed(t *testing.T, ch chan OutputEvent) *Agent {
	s := newTestStore(t)
	sid := newTestSession(t, s, "test-model")
	fp := newFakeProvider()
	return NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, ch, nil)
}

// drainOne returns the next event on ch, or (zero, false) if none is queued.
func drainOne(ch chan OutputEvent) (OutputEvent, bool) {
	select {
	case ev := <-ch:
		return ev, true
	default:
		return OutputEvent{}, false
	}
}

// TestSendInferenceSpeedEvent covers the unit the whole feature rests on: an
// OutputInferenceSpeed event carrying the exact output-token count and a
// tok/s figure derived from real wall-clock elapsed time — and every reason
// it should stay silent instead (see agent.OutputInferenceSpeed doc).
func TestSendInferenceSpeedEvent(t *testing.T) {
	ch := make(chan OutputEvent, 8)
	a := newTestAgentForSpeed(t, ch)

	// 100 output tokens over ~500ms => ~200 tok/s. Backdating roundStart
	// instead of a real sleep keeps this deterministic and instant.
	a.sendInferenceSpeedEvent(&provider.Usage{OutputTokens: 100}, time.Now().Add(-500*time.Millisecond))
	ev, ok := drainOne(ch)
	if !ok {
		t.Fatal("expected an OutputInferenceSpeed event, got none")
	}
	if ev.Type != OutputInferenceSpeed {
		t.Fatalf("Type = %q, want %q", ev.Type, OutputInferenceSpeed)
	}
	if ev.OutputTokens != 100 {
		t.Fatalf("OutputTokens = %d, want 100", ev.OutputTokens)
	}
	if ev.TokensPerSec < 150 || ev.TokensPerSec > 300 {
		t.Fatalf("TokensPerSec = %v, want roughly 200 (100 tokens / ~0.5s)", ev.TokensPerSec)
	}

	// Every "nothing to report" case must still send an event (TokensPerSec
	// left at zero, which the TUI never renders) — never skip the send
	// entirely, or scrollback's pending-round-blocks set (see
	// scrollback.applyInferenceSpeed) would never clear and the next round
	// that DOES have a reading would wrongly retag these blocks too.
	cases := []struct {
		name  string
		usage *provider.Usage
		start time.Time
	}{
		{"nil usage", nil, time.Now().Add(-time.Second)},
		{"zero output tokens", &provider.Usage{OutputTokens: 0}, time.Now().Add(-time.Second)},
		{"round too short", &provider.Usage{OutputTokens: 50}, time.Now()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a.sendInferenceSpeedEvent(c.usage, c.start)
			ev, ok := drainOne(ch)
			if !ok {
				t.Fatal("expected an event even with nothing to report, got none")
			}
			if ev.Type != OutputInferenceSpeed {
				t.Fatalf("Type = %q, want %q", ev.Type, OutputInferenceSpeed)
			}
			if ev.TokensPerSec != 0 || ev.OutputTokens != 0 {
				t.Fatalf("expected zero-value event, got %+v", ev)
			}
		})
	}
}

// withZeroInferenceSpeedFloor temporarily drops minInferenceSpeedElapsed so a
// full runTurn — whose fake-provider round-trip completes near-instantly,
// nowhere near the real 100ms floor — still reports a speed event, proving
// the wiring end-to-end instead of just the unit above. 1ns (not 0) keeps a
// literal zero-elapsed division impossible even in principle.
func withZeroInferenceSpeedFloor(t *testing.T) {
	t.Helper()
	old := minInferenceSpeedElapsed
	minInferenceSpeedElapsed = time.Nanosecond
	t.Cleanup(func() { minInferenceSpeedElapsed = old })
}

// TestInteg_InferenceSpeedTextOnly verifies a plain text turn (no tool calls)
// reports OutputInferenceSpeed with the round's exact output-token count.
func TestInteg_InferenceSpeedTextOnly(t *testing.T) {
	withZeroInferenceSpeedFloor(t)
	e := newIntegEnv(t, [][]provider.StreamEvent{
		provider.FakeTextResponse("hi there", &provider.Usage{InputTokens: 10, OutputTokens: 42}),
	})

	events := e.send("hello")

	var found *OutputEvent
	for i, ev := range events {
		if ev.Type == OutputInferenceSpeed {
			found = &events[i]
		}
	}
	if found == nil {
		t.Fatal("no OutputInferenceSpeed event received")
	}
	if found.OutputTokens != 42 {
		t.Fatalf("OutputTokens = %d, want 42", found.OutputTokens)
	}
	if found.TokensPerSec <= 0 {
		t.Fatalf("TokensPerSec = %v, want > 0", found.TokensPerSec)
	}
}

// TestInteg_InferenceSpeedToolCallRound verifies a round that produces tool
// calls also reports OutputInferenceSpeed, sent after the tool-start events
// so the TUI can tag every block from that round (see
// scrollback.applyInferenceSpeed).
func TestInteg_InferenceSpeedToolCallRound(t *testing.T) {
	withZeroInferenceSpeedFloor(t)
	first, second := provider.FakeToolCallResponse("read", map[string]string{"path": "x.txt"}, "done")
	first[len(first)-1].Usage = &provider.Usage{InputTokens: 10, OutputTokens: 7}
	e := newIntegEnv(t, [][]provider.StreamEvent{first, second})

	events := e.send("read x.txt")

	toolStartIdx, speedIdx := -1, -1
	for i, ev := range events {
		switch ev.Type {
		case OutputToolStart:
			if toolStartIdx == -1 {
				toolStartIdx = i
			}
		case OutputInferenceSpeed:
			if speedIdx == -1 {
				speedIdx = i
				if ev.OutputTokens != 7 {
					t.Errorf("OutputTokens = %d, want 7", ev.OutputTokens)
				}
			}
		}
	}
	if toolStartIdx == -1 {
		t.Fatal("no OutputToolStart event received")
	}
	if speedIdx == -1 {
		t.Fatal("no OutputInferenceSpeed event received")
	}
	if speedIdx < toolStartIdx {
		t.Fatalf("OutputInferenceSpeed (idx %d) arrived before OutputToolStart (idx %d) — TUI would have no tool-call block yet to tag", speedIdx, toolStartIdx)
	}
}

// TestInteg_InferenceSpeedSentEvenWithoutAReading reproduces a real bug: a
// round whose usage isn't reportable (here, zero output tokens on the first
// of two rounds) used to skip sending OutputInferenceSpeed entirely — which
// left that round's blocks marked "pending" in the TUI's scrollback forever
// (see scrollback.applyInferenceSpeed), so the NEXT round's real reading
// would wrongly retag them too. The fix: always send one event per round,
// with TokensPerSec left at zero (never rendered) when there's nothing to
// report — so the TUI's pending-round-blocks bookkeeping still clears.
func TestInteg_InferenceSpeedSentEvenWithoutAReading(t *testing.T) {
	withZeroInferenceSpeedFloor(t)
	first, second := provider.FakeToolCallResponse("read", map[string]string{"path": "x.txt"}, "done")
	// Round 1 (the tool-call round): no reading available.
	first[len(first)-1].Usage = &provider.Usage{InputTokens: 10, OutputTokens: 0}
	// Round 2 (the final-text round): a real reading.
	second[len(second)-1].Usage = &provider.Usage{InputTokens: 5, OutputTokens: 9}
	e := newIntegEnv(t, [][]provider.StreamEvent{first, second})

	events := e.send("read x.txt")

	var speedEvents []OutputEvent
	for _, ev := range events {
		if ev.Type == OutputInferenceSpeed {
			speedEvents = append(speedEvents, ev)
		}
	}
	if len(speedEvents) != 2 {
		t.Fatalf("got %d OutputInferenceSpeed events, want 2 (one per round, even the one with nothing to report)", len(speedEvents))
	}
	if speedEvents[0].TokensPerSec != 0 || speedEvents[0].OutputTokens != 0 {
		t.Fatalf("round 1 (no reading) = %+v, want zero-value", speedEvents[0])
	}
	if speedEvents[1].TokensPerSec <= 0 || speedEvents[1].OutputTokens != 9 {
		t.Fatalf("round 2 (real reading) = %+v, want OutputTokens=9 and TokensPerSec>0", speedEvents[1])
	}
}
