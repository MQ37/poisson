package tools

import (
	"testing"

	"github.com/mq37/poisson/internal/provider"
)

// TestWebUsageFn_RecordSkipsEmptyCalls is the guard against phantom rows: a
// free backend (exa, DuckDuckGo) or a call that never reached the network
// must not leave a zero-cost, zero-token api_calls row behind.
func TestWebUsageFn_RecordSkipsEmptyCalls(t *testing.T) {
	var got []WebCall
	fn := WebUsageFn(func(c WebCall) { got = append(got, c) })

	fn.record(WebCall{}) // no model at all
	fn.record(WebCall{Provider: "anthropic", Model: "claude-haiku-4-5"})
	if len(got) != 0 {
		t.Fatalf("recorded %d calls, want 0: %+v", len(got), got)
	}
}

// TestWebUsageFn_RecordKeepsRealSpend: a call with either tokens or a
// provider-reported cost must reach the sink.
func TestWebUsageFn_RecordKeepsRealSpend(t *testing.T) {
	var got []WebCall
	fn := WebUsageFn(func(c WebCall) { got = append(got, c) })

	fn.record(WebCall{Provider: "anthropic", Model: "claude-haiku-4-5", Usage: provider.Usage{InputTokens: 10}})
	fn.record(WebCall{Provider: "xai", Model: "grok-4.3", Cost: 0.002})
	if len(got) != 2 {
		t.Fatalf("recorded %d calls, want 2: %+v", len(got), got)
	}
}

// TestWebUsageFn_NilFnIsNoop: tools built without a sink (tests, or a
// registry with no agent behind it) must not panic on record.
func TestWebUsageFn_NilFnIsNoop(t *testing.T) {
	var fn WebUsageFn
	fn.record(WebCall{Provider: "anthropic", Model: "claude-haiku-4-5", Usage: provider.Usage{InputTokens: 10}})
}
