package tui

import "testing"

func TestFuzzyScoreSubsequence(t *testing.T) {
	if fuzzyScore("mod", "/model") < 0 {
		t.Fatal("expected match")
	}
	if fuzzyScore("xyz", "/model") >= 0 {
		t.Fatal("expected no match")
	}
}

func TestMatchSlashFuzzy(t *testing.T) {
	got := matchSlashFuzzy("/mod")
	if len(got) == 0 || got[0] != "/model" {
		t.Fatalf("got %v", got)
	}
}

func TestRankFuzzyOrdersBetter(t *testing.T) {
	cands := []string{"/providers", "/model", "/models", "/cost"}
	got := rankFuzzy("mod", cands, 10)
	if len(got) < 2 || got[0] != "/model" {
		t.Fatalf("got %v", got)
	}
}