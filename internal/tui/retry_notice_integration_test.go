package tui

import (
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/provider"
)

// TestTUIInteg_RetryNoticeThenFinalTextRenders exercises the full network-
// resilience chain from the provider layer up through paint: FakeProvider
// simulates a real DoWithRetry-style retry (via SetRetryScript, firing the
// same RetryTrace callbacks a real provider's retry loop would against the
// context streamWithRetryNotice attaches), the agent translates that into
// OutputRetrying events, and the TUI renders them as neutral inline notices
// before the turn's actual final text — with no real network involved. This
// is the layer retry_notice_test.go / retry_notice2_test.go (agent-only)
// never exercised: whether the notice actually reaches the screen and the
// turn still completes normally afterward.
func TestTUIInteg_RetryNoticeThenFinalTextRenders(t *testing.T) {
	e := newTUIIntegEnv(t, [][]provider.StreamEvent{
		provider.FakeTextResponse("all better now", nil),
	})
	e.prov.SetRetryScript([]provider.RetryScriptStep{
		{Attempt: 1, Reason: "dial tcp: connection refused"},
		{Attempt: 2, Reason: "dial tcp: connection refused"},
	}, true)

	e.runTurn("hi there")

	text := e.scrollText()
	if !strings.Contains(text, "connection lost: dial tcp: connection refused — reconnecting…") {
		t.Errorf("scrollback missing the retry-start notice, got:\n%s", text)
	}
	if !strings.Contains(text, "reconnected — resuming") {
		t.Errorf("scrollback missing the recovery notice, got:\n%s", text)
	}
	if !strings.Contains(text, "all better now") {
		t.Errorf("scrollback missing the turn's final text after recovery, got:\n%s", text)
	}

	// Exactly one start + one recovered notice, never one per backoff attempt
	// (attempt 1 and 2 above must collapse into a single start notice).
	if n := strings.Count(text, "connection lost:"); n != 1 {
		t.Errorf("expected exactly one retry-start notice, got %d in:\n%s", n, text)
	}

	// The final rendered screen (real paint pipeline) must show the same
	// text, confirming it's not just in the raw scrollback buffer but
	// actually painted.
	screen := e.render()
	if !strings.Contains(screen, "all better now") {
		t.Errorf("rendered screen missing final text, got:\n%s", screen)
	}
}

// TestTUIInteg_RetryNoticeSuppressedWithoutOutage verifies FakeProvider's
// retry-script feature itself is a no-op unless invoked — a normal turn
// with no configured retry script must show no retry notices at all, since
// most turns in most tests should not accidentally trip this code path.
func TestTUIInteg_RetryNoticeSuppressedWithoutOutage(t *testing.T) {
	e := newTUIIntegEnv(t, [][]provider.StreamEvent{
		provider.FakeTextResponse("plain response", nil),
	})
	e.runTurn("hi there")

	text := e.scrollText()
	if strings.Contains(text, "reconnecting") || strings.Contains(text, "resuming") {
		t.Errorf("no retry script was configured, but scrollback shows a retry notice:\n%s", text)
	}
}
