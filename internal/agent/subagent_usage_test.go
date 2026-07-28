package agent

import (
	"testing"

	"github.com/mq37/poisson/internal/config"
	"github.com/mq37/poisson/internal/provider"
)

// TestCumulativeUsageAccumulatesAcrossTurns verifies CumulativeUsage() sums
// every api_calls row recordAPICallFor writes for this Agent's session,
// across multiple turns — this in-memory running total is what subagent/
// child mode relays to its parent on every progress tick (see
// forwardChildEvents in cmd/px/main.go), so it must track real recorded
// usage exactly, not just "roughly".
func TestCumulativeUsageAccumulatesAcrossTurns(t *testing.T) {
	e := newIntegEnv(t, [][]provider.StreamEvent{
		provider.FakeTextResponse("answer 1", &provider.Usage{InputTokens: 100, OutputTokens: 50}),
		provider.FakeTextResponse("answer 2", &provider.Usage{InputTokens: 200, OutputTokens: 100, CacheReadTokens: 10}),
	})

	e.send("q1")
	e.send("q2")

	got := e.agent.CumulativeUsage()
	want := provider.Usage{InputTokens: 300, OutputTokens: 150, CacheReadTokens: 10}
	if got != want {
		t.Fatalf("CumulativeUsage() = %+v, want %+v", got, want)
	}
}

// TestRecordSubagentUsageRollsIntoParentSessionCost is the regression test
// for the bug this feature fixes: a subagent's spend used to be computed
// once (inside the child's own ephemeral, throwaway DB) and then vanish —
// never counted in the parent session's GetSessionCost/
// GetSessionTokenBreakdown. RecordSubagentUsage is what SubagentTool.Execute
// calls (via the usageFn callback) once a child reports its usage; verify it
// lands as an additive "subagent"-purpose api_calls row on the PARENT
// session, priced with the PARENT's own pricing config.
func TestRecordSubagentUsageRollsIntoParentSessionCost(t *testing.T) {
	e := newIntegEnv(t, [][]provider.StreamEvent{
		provider.FakeTextResponse("answer 1", &provider.Usage{InputTokens: 100, OutputTokens: 50}),
	})
	e.agent.Config().Pricing["fake"] = map[string]config.Pricing{
		"test-model": {InputPerMTok: 1.0, OutputPerMTok: 2.0},
	}

	e.send("q1")

	costBefore, err := e.store.GetSessionCost(e.sid)
	if err != nil {
		t.Fatalf("GetSessionCost (before): %v", err)
	}
	if costBefore <= 0 {
		t.Fatalf("costBefore = %v, want positive (main turn should already be priced)", costBefore)
	}

	subagentUsage := &provider.Usage{InputTokens: 500, OutputTokens: 300}
	// childCost 0 = the child reported none, so the parent prices the blob.
	subagentCost, err := e.agent.RecordSubagentUsage("fake", "test-model", subagentUsage, 0)
	if err != nil {
		t.Fatalf("RecordSubagentUsage: %v", err)
	}
	wantSubagentCost := 500.0/1_000_000*1.0 + 300.0/1_000_000*2.0
	if diff := subagentCost - wantSubagentCost; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("subagentCost = %v, want %v", subagentCost, wantSubagentCost)
	}

	costAfter, err := e.store.GetSessionCost(e.sid)
	if err != nil {
		t.Fatalf("GetSessionCost (after): %v", err)
	}
	if diff := costAfter - costBefore - subagentCost; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("costAfter (%v) != costBefore (%v) + subagentCost (%v)", costAfter, costBefore, subagentCost)
	}

	breakdown, err := e.store.GetSessionTokenBreakdown(e.sid)
	if err != nil {
		t.Fatalf("GetSessionTokenBreakdown: %v", err)
	}
	if breakdown.CallCount != 2 {
		t.Fatalf("CallCount = %d, want 2 (1 main turn + 1 subagent rollup)", breakdown.CallCount)
	}
	if breakdown.InputTokens != 600 || breakdown.OutputTokens != 350 {
		t.Fatalf("breakdown tokens = %+v, want input=600 output=350 (100+500, 50+300)", breakdown)
	}

	// CumulativeUsage() must NOT include the subagent rollup: it tracks only
	// what THIS agent recorded via recordAPICallFor for its own turns (main/
	// compaction/aux) — RecordSubagentUsage deliberately bypasses that path,
	// since it's recording on behalf of a child that isn't this Agent.
	if got := e.agent.CumulativeUsage(); got.InputTokens != 100 || got.OutputTokens != 50 {
		t.Fatalf("CumulativeUsage() = %+v, want only the main turn's usage (subagent rollup must not leak in)", got)
	}
}

// TestRecordSubagentUsagePrefersChildReportedCost covers a run that mixed
// models inside the child — the common case being bash-risk classification
// under a /classifier-model pin, which the parent cannot price from a lump
// token total because it only knows the child's MAIN model. The child prices
// each of its own calls correctly and reports the sum, so that figure must be
// recorded verbatim rather than recomputed.
func TestRecordSubagentUsagePrefersChildReportedCost(t *testing.T) {
	e := newIntegEnv(t, [][]provider.StreamEvent{
		provider.FakeTextResponse("answer 1", &provider.Usage{InputTokens: 100, OutputTokens: 50}),
	})
	e.agent.Config().Pricing["fake"] = map[string]config.Pricing{
		"test-model": {InputPerMTok: 1.0, OutputPerMTok: 2.0},
	}
	e.send("q1")

	costBefore, err := e.store.GetSessionCost(e.sid)
	if err != nil {
		t.Fatalf("GetSessionCost (before): %v", err)
	}

	// Deliberately unequal to what pricing "fake"/"test-model" would yield for
	// these tokens ($0.0011), so recomputation is distinguishable.
	const childCost = 0.0075
	usage := &provider.Usage{InputTokens: 500, OutputTokens: 300}
	got, err := e.agent.RecordSubagentUsage("fake", "test-model", usage, childCost)
	if err != nil {
		t.Fatalf("RecordSubagentUsage: %v", err)
	}
	if got != childCost {
		t.Fatalf("returned cost = %v, want the child's own %v (not a reprice)", got, childCost)
	}

	costAfter, err := e.store.GetSessionCost(e.sid)
	if err != nil {
		t.Fatalf("GetSessionCost (after): %v", err)
	}
	if diff := costAfter - costBefore - childCost; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("session cost grew by %v, want exactly the child's reported %v", costAfter-costBefore, childCost)
	}
}

// TestCumulativeCostTracksEveryRecordedCall verifies the running cost total
// child mode relays to its parent (see CumulativeCost) actually accumulates,
// so a subagent's spend is reported at what the child paid.
func TestCumulativeCostTracksEveryRecordedCall(t *testing.T) {
	e := newIntegEnv(t, [][]provider.StreamEvent{
		provider.FakeTextResponse("a1", &provider.Usage{InputTokens: 100, OutputTokens: 50}),
		provider.FakeTextResponse("a2", &provider.Usage{InputTokens: 200, OutputTokens: 100}),
	})
	e.agent.Config().Pricing["fake"] = map[string]config.Pricing{
		"test-model": {InputPerMTok: 1.0, OutputPerMTok: 2.0},
	}

	if got := e.agent.CumulativeCost(); got != 0 {
		t.Fatalf("CumulativeCost() before any turn = %v, want 0", got)
	}
	e.send("q1")
	e.send("q2")

	// Must equal the session's own SUM(cost) — same rows, same arithmetic.
	want, err := e.store.GetSessionCost(e.sid)
	if err != nil {
		t.Fatalf("GetSessionCost: %v", err)
	}
	if want <= 0 {
		t.Fatalf("session cost = %v, want positive", want)
	}
	if diff := e.agent.CumulativeCost() - want; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("CumulativeCost() = %v, want the session's SUM(cost) %v", e.agent.CumulativeCost(), want)
	}
}
