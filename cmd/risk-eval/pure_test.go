package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// --- loadCases ---

func TestLoadCasesDefaultsVersionWhenZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cases.json")
	data := `{"cases":[{"id":"c1","category":"safe"}]}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	suite, err := loadCases(path)
	if err != nil {
		t.Fatalf("loadCases: %v", err)
	}
	if suite.Version != 1 {
		t.Errorf("Version = %d, want 1 (default applied when omitted)", suite.Version)
	}
}

// TestLoadCasesDefaultsWorkdirWhenEmpty pins the fallback workdir applied
// when the fixture's "defaults.workdir" is omitted/empty.
func TestLoadCasesDefaultsWorkdirWhenEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cases.json")
	data := `{"version":2,"defaults":{"description":"d"},"cases":[]}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	suite, err := loadCases(path)
	if err != nil {
		t.Fatalf("loadCases: %v", err)
	}
	if suite.Version != 2 {
		t.Errorf("Version = %d, want 2 (explicit value preserved)", suite.Version)
	}
	if suite.Defaults.Workdir != "/tmp/poisson-eval" {
		t.Errorf("Defaults.Workdir = %q, want the fallback", suite.Defaults.Workdir)
	}
}

func TestLoadCasesPreservesExplicitWorkdir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cases.json")
	data := `{"version":1,"defaults":{"workdir":"/srv/custom"},"cases":[]}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	suite, err := loadCases(path)
	if err != nil {
		t.Fatalf("loadCases: %v", err)
	}
	if suite.Defaults.Workdir != "/srv/custom" {
		t.Errorf("Defaults.Workdir = %q, want the explicit value preserved", suite.Defaults.Workdir)
	}
}

func TestLoadCasesMissingFileErrors(t *testing.T) {
	if _, err := loadCases(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

// --- filterCases ---

func sampleCases() []evalCase {
	return []evalCase{
		{ID: "safe-001", Category: "safe", Tags: []string{"read", "fs"}},
		{ID: "safe-002", Category: "safe", Tags: []string{"write"}},
		{ID: "risky-001", Category: "risky", Tags: []string{"read", "net"}},
		{ID: "risky-002", Category: "risky", Tags: []string{"net"}},
	}
}

// TestFilterCasesIsRealANDNotOR proves category+tag combine with AND: a
// case matching only one of the two filters must be excluded, not included
// (which an OR implementation would incorrectly do).
func TestFilterCasesIsRealANDNotOR(t *testing.T) {
	got := filterCases(sampleCases(), "safe", "net", "")
	if len(got) != 0 {
		t.Errorf("got %+v, want none — safe-* cases have no \"net\" tag, risky-* aren't category \"safe\"", got)
	}

	got2 := filterCases(sampleCases(), "risky", "net", "")
	want2 := []evalCase{
		{ID: "risky-001", Category: "risky", Tags: []string{"read", "net"}},
		{ID: "risky-002", Category: "risky", Tags: []string{"net"}},
	}
	if !reflect.DeepEqual(got2, want2) {
		t.Errorf("got %+v, want %+v", got2, want2)
	}
}

// TestFilterCasesIDIsPrefixMatchNotExact proves the id filter matches by
// prefix (strings.HasPrefix), not exact equality — "safe-00" must match
// both safe-001 and safe-002.
func TestFilterCasesIDIsPrefixMatchNotExact(t *testing.T) {
	got := filterCases(sampleCases(), "", "", "safe-00")
	if len(got) != 2 {
		t.Fatalf("got %d cases, want 2 (prefix match)", len(got))
	}
	for _, c := range got {
		if c.Category != "safe" {
			t.Errorf("unexpected case in prefix match result: %+v", c)
		}
	}

	// An id that happens to equal one case's full id exactly still only
	// matches that one, but via prefix — not because it fails on non-exact.
	exact := filterCases(sampleCases(), "", "", "risky-001")
	if len(exact) != 1 || exact[0].ID != "risky-001" {
		t.Errorf("got %+v, want exactly risky-001", exact)
	}
}

func TestFilterCasesNoFiltersReturnsAll(t *testing.T) {
	got := filterCases(sampleCases(), "", "", "")
	if len(got) != 4 {
		t.Errorf("got %d cases, want all 4 with no filters applied", len(got))
	}
}

// --- hasTag ---

func TestHasTagExactMatch(t *testing.T) {
	tags := []string{"read", "network"}
	if !hasTag(tags, "read") {
		t.Error("hasTag(\"read\") = false, want true")
	}
	if hasTag(tags, "net") {
		t.Error("hasTag(\"net\") = true, want false — must be an exact match, not a substring")
	}
	if hasTag(tags, "") {
		t.Error("hasTag(\"\") = true, want false")
	}
}

// --- exitCodeFromReport ---

func writeFixtureReport(t *testing.T, failed int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "report.json")
	rep := report{Summary: summary{Total: failed + 1, Passed: 1, Failed: failed}}
	data, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// TestExitCodeFromReportZeroFailedIsZero pins the CI success case: a report
// with Failed:0 must exit 0.
func TestExitCodeFromReportZeroFailedIsZero(t *testing.T) {
	path := writeFixtureReport(t, 0)
	if got := exitCodeFromReport(path); got != 0 {
		t.Errorf("exitCodeFromReport = %d, want 0 for Failed:0", got)
	}
}

// TestExitCodeFromReportOneFailedIsOne pins the CI gate itself: any failure
// at all must trip a nonzero exit code.
func TestExitCodeFromReportOneFailedIsOne(t *testing.T) {
	path := writeFixtureReport(t, 1)
	if got := exitCodeFromReport(path); got != 1 {
		t.Errorf("exitCodeFromReport = %d, want 1 for Failed:1", got)
	}
}

func TestExitCodeFromReportMissingFileIsOne(t *testing.T) {
	if got := exitCodeFromReport(filepath.Join(t.TempDir(), "nope.json")); got != 1 {
		t.Errorf("exitCodeFromReport = %d, want 1 for an unreadable path", got)
	}
}

func TestExitCodeFromReportMalformedJSONIsOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if got := exitCodeFromReport(path); got != 1 {
		t.Errorf("exitCodeFromReport = %d, want 1 for malformed JSON", got)
	}
}

// --- writeReport ---

func TestWriteReportRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.json")
	rep := report{
		Provider: "anthropic", Model: "claude-sonnet-5", Mode: "full",
		Summary: summary{Total: 2, Passed: 1, Failed: 1, ByCategory: map[string]catSummary{"safe": {Total: 2, Passed: 1}}},
	}

	if err := writeReport(path, rep); err != nil {
		t.Fatalf("writeReport: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read written report: %v", err)
	}
	var got report
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("re-parse written report: %v", err)
	}
	if !reflect.DeepEqual(got, rep) {
		t.Errorf("round-tripped report = %+v, want %+v", got, rep)
	}
}

// TestWriteReportCreatesNestedDir forces the MkdirAll path: --out pointing
// at a directory that doesn't exist yet must still succeed.
func TestWriteReportCreatesNestedDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deep", "report.json")
	if err := writeReport(path, report{}); err != nil {
		t.Fatalf("writeReport into a nested new dir: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("report file not written: %v", err)
	}
}
