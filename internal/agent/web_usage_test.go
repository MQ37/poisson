package agent

import (
	"testing"

	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/tools"
)

// TestRecordWebToolCall_PricesFromRateTableWhenNoCostGiven covers the
// Anthropic path: the backend reports tokens and a search-request count, not
// a dollar figure, so RecordWebToolCall must price both the tokens (against
// the helper model's own rate, not the session model's) and the per-search
// fee, and bank the row under the tool's purpose rather than "main".
func TestRecordWebToolCall_PricesFromRateTableWhenNoCostGiven(t *testing.T) {
	s := newTestStore(t)
	sid := newTestSession(t, s, "test-model")
	a := NewAgent(s, newFakeProvider(), newTestRegistry("."), newTestConfig(), sid, make(chan OutputEvent, 8), nil)

	a.RecordWebToolCall(tools.WebCall{
		Purpose:        "web_search",
		Provider:       "anthropic",
		Model:          "claude-haiku-4-5-20251001",
		Usage:          provider.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000},
		SearchRequests: 2,
	})

	cost, err := s.GetSessionCost(sid)
	if err != nil {
		t.Fatalf("GetSessionCost: %v", err)
	}
	// 1M input @ $1/MTok + 1M output @ $5/MTok + 2 searches @ $0.01 = 6.02.
	if want := 6.02; cost < want-1e-9 || cost > want+1e-9 {
		t.Fatalf("cost = %v, want %v", cost, want)
	}

	call, err := s.GetLastAPICall(sid)
	// GetLastAPICall only returns purpose='main' rows — a web_search-purpose
	// row must NOT satisfy it (that would corrupt the context-window anchor
	// with a tiny helper call's usage).
	if err == nil {
		t.Fatalf("GetLastAPICall found a row (%+v), want none for a non-main purpose", call)
	}

	breakdown, err := s.GetSessionTokenBreakdown(sid)
	if err != nil {
		t.Fatalf("GetSessionTokenBreakdown: %v", err)
	}
	if breakdown.CallCount != 1 || breakdown.InputTokens != 1_000_000 {
		t.Fatalf("breakdown = %+v", breakdown)
	}
}

// TestRecordWebToolCall_UsesProviderReportedCostVerbatim covers web_ask's Grok
// path: xAI reports its own exact cost_in_usd_ticks figure, which must be
// recorded as-is rather than re-priced from the local rate table (which,
// among other things, has no way to model xAI's own tool fee).
func TestRecordWebToolCall_UsesProviderReportedCostVerbatim(t *testing.T) {
	s := newTestStore(t)
	sid := newTestSession(t, s, "test-model")
	a := NewAgent(s, newFakeProvider(), newTestRegistry("."), newTestConfig(), sid, make(chan OutputEvent, 8), nil)

	a.RecordWebToolCall(tools.WebCall{
		Purpose:  "web_ask",
		Provider: "xai",
		Model:    "grok-4.3",
		Usage:    provider.Usage{InputTokens: 999_999_999, OutputTokens: 999_999_999}, // would price huge if used
		Cost:     0.0071,
	})

	cost, err := s.GetSessionCost(sid)
	if err != nil {
		t.Fatalf("GetSessionCost: %v", err)
	}
	if cost != 0.0071 {
		t.Fatalf("cost = %v, want the verbatim 0.0071 (rate table must be ignored)", cost)
	}
}

// TestReloadConfigDependentTools_WiresWebUsageSink is the wiring gate:
// fetch/web_search/web_ask on the registry must all satisfy the SetUsageFn
// interface tools.BindWebUsage relies on after ReloadConfigDependentTools —
// end-to-end recording through each concrete backend is covered where the
// backend itself lives (tools.TestWebAskTool_GrokRecordsSpend,
// tools.TestWebSearch_AnthropicProviderUsesBackend,
// tools.TestFetch_AnthropicProviderFetchesLocallyThenSummarizes) and
// BindWebUsage's own wiring in tools.TestBindWebUsage.
func TestReloadConfigDependentTools_WiresWebUsageSink(t *testing.T) {
	s := newTestStore(t)
	sid := newTestSession(t, s, "test-model")
	reg := tools.BuildRegistry(tools.BuildOptions{Cwd: "."})
	a := NewAgent(s, newFakeProvider(), reg, newTestConfig(), sid, make(chan OutputEvent, 8), nil)
	a.ReloadConfigDependentTools()

	for _, name := range []string{"fetch", "web_search", "web_ask"} {
		tl, ok := reg.Get(name)
		if !ok {
			t.Fatalf("tool %q not registered", name)
		}
		if _, ok := tl.(interface{ SetUsageFn(tools.WebUsageFn) }); !ok {
			t.Errorf("tool %q does not implement SetUsageFn, BindWebUsage cannot wire it", name)
		}
	}
}
