package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestMatchAtFileFuzzyPrefixWinsOverFuzzyScore locks in the fork in
// matchAtFileFuzzy: when any candidate matches the query as a literal
// filename prefix, prefix hits win outright and fuzzy-ranked candidates
// (even ones that would score well as a subsequence match) are excluded
// entirely, not merged in or reordered underneath the prefix hits.
func TestMatchAtFileFuzzyPrefixWinsOverFuzzyScore(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"foo.go", "foobar.go", "zfoo.go"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, truncated := matchAtFileFuzzy("@foo", dir)
	if truncated {
		t.Fatal("unexpected truncation")
	}
	sort.Strings(got)
	want := []string{"@foo.go", "@foobar.go"}
	sort.Strings(want)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v, want %v (zfoo.go must NOT appear via fuzzy scoring once prefix hits exist)", got, want)
	}
	for _, g := range got {
		if g == "@zfoo.go" {
			t.Fatal("zfoo.go leaked in despite the prefix-hit fork")
		}
	}
}

// TestMatchAtFileFuzzyDotfileFiltering: dotfiles are excluded unless the
// query itself starts with a dot.
func TestMatchAtFileFuzzyDotfileFiltering(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "visible.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, _ := matchAtFileFuzzy("@", dir)
	for _, g := range got {
		if g == "@.env" || g == "@.git/" {
			t.Fatalf("dotfile %q leaked into no-dot query results: %v", g, got)
		}
	}
	found := false
	for _, g := range got {
		if g == "@visible.go" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected @visible.go in %v", got)
	}

	got2, _ := matchAtFileFuzzy("@.env", dir)
	found = false
	for _, g := range got2 {
		if g == "@.env" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected @.env when query starts with a dot, got %v", got2)
	}
}

// TestMatchAtFileFuzzyTruncatesAtCap confirms the fuzzyResultCap truncation
// flag fires once fuzzy-ranked matches exceed the cap.
func TestMatchAtFileFuzzyTruncatesAtCap(t *testing.T) {
	dir := t.TempDir()
	n := fuzzyResultCap + 10
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("data%02d.txt", i) // none start with "a" -> no prefix hits
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, truncated := matchAtFileFuzzy("@a", dir)
	if !truncated {
		t.Fatal("expected truncated=true with more matches than the cap")
	}
	if len(got) != fuzzyResultCap {
		t.Fatalf("got %d results, want cap %d", len(got), fuzzyResultCap)
	}
}
