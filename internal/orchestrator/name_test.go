package orchestrator

import (
	"regexp"
	"strings"
	"testing"
)

// nameFormat is the exact contract ResolveInstanceName must satisfy,
// verbatim from docs/orchestrator-plan.md Step 13's verify criterion.
var nameFormat = regexp.MustCompile(`^px-[a-z0-9][a-z0-9-]{0,28}[a-z0-9]$`)

// TestResolveInstanceName_AdversarialInputs table-tests against genuinely
// adversarial input, not just happy-path names — every case must still
// produce output matching nameFormat.
func TestResolveInstanceName_AdversarialInputs(t *testing.T) {
	cases := []string{
		"",
		"../../etc",
		"A_B",
		strings.Repeat("x", 200),
		"🔥🔥🔥emoji-only-ish🔥🔥🔥",
		"----",
		"a",         // single surviving char after sanitize
		"  spaced  ",
		"Valid-Name-1",
		"UPPER_CASE_NAME",
	}
	for _, requested := range cases {
		got, err := ResolveInstanceName(requested)
		if err != nil {
			t.Errorf("ResolveInstanceName(%q) error: %v", requested, err)
			continue
		}
		if !nameFormat.MatchString(got) {
			t.Errorf("ResolveInstanceName(%q) = %q, does not match %s", requested, got, nameFormat)
		}
	}
}

// TestResolveInstanceName_UnderscoreMapsToDash is the explicit reuse-
// decision-9 regression guard: unlike sandbox.ResolveSandboxName,
// underscores must not survive — nspawn machine names are RFC1123
// hostnames.
func TestResolveInstanceName_UnderscoreMapsToDash(t *testing.T) {
	got, err := ResolveInstanceName("my_instance_name")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "_") {
		t.Errorf("got %q, want no underscores", got)
	}
	if got != "px-my-instance-name" {
		t.Errorf("got %q, want px-my-instance-name", got)
	}
}

// TestResolveInstanceName_EmptyGetsRandomFallback checks empty input
// produces a usable, distinct name each call (not a fixed/empty string).
func TestResolveInstanceName_EmptyGetsRandomFallback(t *testing.T) {
	a, err := ResolveInstanceName("")
	if err != nil {
		t.Fatal(err)
	}
	b, err := ResolveInstanceName("")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Errorf("two empty-input calls produced the same name %q, want random", a)
	}
	if !nameFormat.MatchString(a) || !nameFormat.MatchString(b) {
		t.Errorf("fallback names %q/%q don't match %s", a, b, nameFormat)
	}
}

// TestResolveInstanceName_CollapsesRepeatedDashes checks internal dash runs
// (not just leading/trailing) collapse to one, matching sandbox's own
// sanitizer shape.
func TestResolveInstanceName_CollapsesRepeatedDashes(t *testing.T) {
	got, err := ResolveInstanceName("foo///bar")
	if err != nil {
		t.Fatal(err)
	}
	if got != "px-foo-bar" {
		t.Errorf("got %q, want px-foo-bar", got)
	}
}

// TestResolveInstanceName_CapsLength checks a name long enough to need
// truncation still matches the format (never overruns maxBodyLen).
func TestResolveInstanceName_CapsLength(t *testing.T) {
	got, err := ResolveInstanceName(strings.Repeat("ab", 100))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > len(instanceNamePrefix)+maxBodyLen {
		t.Errorf("got %q (len %d), want at most %d chars", got, len(got), len(instanceNamePrefix)+maxBodyLen)
	}
	if !nameFormat.MatchString(got) {
		t.Errorf("got %q, does not match %s", got, nameFormat)
	}
}
