package main

import (
	"reflect"
	"testing"
)

// --- buildCombos ---

func TestBuildCombosEmptyMainsYieldsNoCombos(t *testing.T) {
	if got := buildCombos(nil, []string{"claude-haiku"}); got != nil {
		t.Errorf("got %+v, want nil for no main models", got)
	}
	if got := buildCombos([]string{}, nil); got != nil {
		t.Errorf("got %+v, want nil for empty mains slice", got)
	}
}

// TestBuildCombosSkipsBlankAndWhitespaceMains covers the trailing-comma case
// (strings.Split("a,b,", ",") yields a trailing "") and a whitespace-only
// entry, neither of which should produce a combo.
func TestBuildCombosSkipsBlankAndWhitespaceMains(t *testing.T) {
	got := buildCombos([]string{"claude-sonnet-5", "", "   ", "claude-opus-5"}, nil)
	want := []combo{{main: "claude-sonnet-5"}, {main: "claude-opus-5"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// TestBuildCombosSkipsClassifierEqualToMain proves a classifier identical to
// its own main model is skipped (that combo is identical to the bare
// "(inherit)" one already produced), while a genuinely different classifier
// still produces its own combo.
func TestBuildCombosSkipsClassifierEqualToMain(t *testing.T) {
	got := buildCombos([]string{"claude-opus-5"}, []string{"claude-opus-5", "claude-haiku"})
	want := []combo{
		{main: "claude-opus-5"},
		{main: "claude-opus-5", classifier: "claude-haiku"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v (self-classifier combo must be skipped)", got, want)
	}
}

// TestBuildCombosEveryMainAlwaysGetsInheritCombo pins the "(inherit)" case:
// every main model gets a bare combo (empty classifier) first, regardless of
// how many classifier overrides are also requested.
func TestBuildCombosEveryMainAlwaysGetsInheritCombo(t *testing.T) {
	got := buildCombos([]string{"m1", "m2"}, []string{"c1"})
	want := []combo{
		{main: "m1"}, {main: "m1", classifier: "c1"},
		{main: "m2"}, {main: "m2", classifier: "c1"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// --- splitNonEmpty ---

func TestSplitNonEmptyEmptyStringYieldsNil(t *testing.T) {
	if got := splitNonEmpty(""); got != nil {
		t.Errorf("got %+v, want nil", got)
	}
}

// TestSplitNonEmptyFiltersWhitespaceAndTrailingComma covers whitespace-only
// entries and a trailing comma (the shape a real "--classifier a,b," flag
// value produces via strings.Split).
func TestSplitNonEmptyFiltersWhitespaceAndTrailingComma(t *testing.T) {
	got := splitNonEmpty("claude-haiku, claude-sonnet-5 ,   ,")
	want := []string{"claude-haiku", "claude-sonnet-5"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// --- classifierLabel ---

func TestClassifierLabelEmptyIsInherit(t *testing.T) {
	if got := classifierLabel(""); got != "(inherit)" {
		t.Errorf("got %q, want %q", got, "(inherit)")
	}
}

func TestClassifierLabelNonEmptyPassesThrough(t *testing.T) {
	if got := classifierLabel("claude-haiku"); got != "claude-haiku" {
		t.Errorf("got %q, want %q", got, "claude-haiku")
	}
}
