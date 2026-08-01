package main

import "testing"

// TestRecordCaseSummaryCountsOneOutcomePerCaseNotPerAttempt is the
// regression test for the retries-counted-as-separate-cases bug: a case
// that fails on its first attempt(s) but ultimately passes on retry must
// tally as exactly one PASS, not a mix of pass+fail entries that inflate
// Total and leave Failed>0 (which would trip main's exit-code gate even
// though the case's real outcome was a pass).
func TestRecordCaseSummaryCountsOneOutcomePerCaseNotPerAttempt(t *testing.T) {
	var rep report
	rep.Summary.ByCategory = map[string]catSummary{}

	// Simulates runCase's own output shape: fails twice, passes on the
	// 3rd (and final, since runCase stops retrying at the first pass) attempt.
	runs := []caseRun{
		{ID: "c1", Category: "cat-a", Attempt: 1, Pass: false},
		{ID: "c1", Category: "cat-a", Attempt: 2, Pass: false},
		{ID: "c1", Category: "cat-a", Attempt: 3, Pass: true},
	}
	recordCaseSummary(&rep, runs)

	if rep.Summary.Total != 1 {
		t.Errorf("Total = %d, want 1 (one case, not one per attempt)", rep.Summary.Total)
	}
	if rep.Summary.Passed != 1 {
		t.Errorf("Passed = %d, want 1", rep.Summary.Passed)
	}
	if rep.Summary.Failed != 0 {
		t.Errorf("Failed = %d, want 0 — case ultimately passed on retry, exit-code gate must not fire for it", rep.Summary.Failed)
	}
	if len(rep.Runs) != 3 {
		t.Errorf("rep.Runs = %d entries, want all 3 attempts preserved for retry-history visibility", len(rep.Runs))
	}
	cs := rep.Summary.ByCategory["cat-a"]
	if cs.Total != 1 || cs.Passed != 1 {
		t.Errorf("ByCategory[cat-a] = %+v, want {Total:1 Passed:1}", cs)
	}
}

// TestRecordCaseSummaryAllAttemptsFailCountsOneFailure verifies a case that
// exhausts every retry without ever passing still tallies as exactly one
// failure (not one per attempt), taking Critical from the final attempt.
func TestRecordCaseSummaryAllAttemptsFailCountsOneFailure(t *testing.T) {
	var rep report
	rep.Summary.ByCategory = map[string]catSummary{}

	runs := []caseRun{
		{ID: "c2", Category: "cat-b", Attempt: 1, Pass: false, Critical: false},
		{ID: "c2", Category: "cat-b", Attempt: 2, Pass: false, Critical: true},
	}
	recordCaseSummary(&rep, runs)

	if rep.Summary.Total != 1 {
		t.Errorf("Total = %d, want 1", rep.Summary.Total)
	}
	if rep.Summary.Failed != 1 {
		t.Errorf("Failed = %d, want 1 (not one per exhausted attempt)", rep.Summary.Failed)
	}
	if rep.Summary.Critical != 1 {
		t.Errorf("Critical = %d, want 1 (from the final attempt)", rep.Summary.Critical)
	}
	if len(rep.Runs) != 2 {
		t.Errorf("rep.Runs = %d entries, want both attempts preserved", len(rep.Runs))
	}
}

// TestRecordCaseSummaryFirstAttemptPassSingleRun verifies the common case
// (no retry needed at all) still works: exactly one run, tallied once.
func TestRecordCaseSummaryFirstAttemptPassSingleRun(t *testing.T) {
	var rep report
	rep.Summary.ByCategory = map[string]catSummary{}

	recordCaseSummary(&rep, []caseRun{{ID: "c3", Category: "cat-a", Attempt: 1, Pass: true}})

	if rep.Summary.Total != 1 || rep.Summary.Passed != 1 || rep.Summary.Failed != 0 {
		t.Errorf("summary = %+v, want {Total:1 Passed:1 Failed:0}", rep.Summary)
	}
}

// TestRecordCaseSummaryAccumulatesAcrossMultipleCases verifies the summary
// counters accumulate correctly across several cases in sequence (the real
// main() loop calls recordCaseSummary once per case).
func TestRecordCaseSummaryAccumulatesAcrossMultipleCases(t *testing.T) {
	var rep report
	rep.Summary.ByCategory = map[string]catSummary{}

	recordCaseSummary(&rep, []caseRun{{ID: "a", Category: "x", Pass: true}})
	recordCaseSummary(&rep, []caseRun{
		{ID: "b", Category: "x", Pass: false},
		{ID: "b", Category: "x", Pass: false},
	})
	recordCaseSummary(&rep, []caseRun{{ID: "c", Category: "y", Pass: true}})

	if rep.Summary.Total != 3 {
		t.Errorf("Total = %d, want 3", rep.Summary.Total)
	}
	if rep.Summary.Passed != 2 || rep.Summary.Failed != 1 {
		t.Errorf("Passed=%d Failed=%d, want 2/1", rep.Summary.Passed, rep.Summary.Failed)
	}
	if rep.Summary.ByCategory["x"].Total != 2 || rep.Summary.ByCategory["y"].Total != 1 {
		t.Errorf("ByCategory = %+v, want x:2 y:1", rep.Summary.ByCategory)
	}
}
