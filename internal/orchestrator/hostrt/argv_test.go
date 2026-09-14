package hostrt

import (
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/orchestrator"
)

func testConfig() Config {
	return Config{PxBinPath: "/usr/local/bin/px", StateRoot: "/var/lib/px-orchestrate"}
}

func assertArgsEqual(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("args = %v, want %v (differ at index %d)", got, want, i)
		}
	}
}

func TestTurnUnitName(t *testing.T) {
	if got, want := turnUnitName("px-alpha"), "px-turn-px-alpha"; got != want {
		t.Errorf("turnUnitName = %q, want %q", got, want)
	}
}

// TestTurnRunArgs_NoMachineFlag is the one thing that distinguishes this
// from nspawn's own turnRunArgs -- everything else is identical.
func TestTurnRunArgs_NoMachineFlag(t *testing.T) {
	spec := orchestrator.TurnSpec{SessionID: "s-1", Provider: "anthropic", Model: "claude-sonnet-5", Message: "hello there"}
	got := turnRunArgs(testConfig(), "px-alpha", spec)
	for _, a := range got {
		if strings.HasPrefix(a, "--machine=") {
			t.Errorf("args = %v, want no --machine= flag (host mode has no container)", got)
		}
	}
	want := []string{
		"--unit=px-turn-px-alpha", "--pipe", "--collect", "--wait",
		"--setenv=HOME=/root", "--setenv=POISSON_ORCHESTRATE_INSTANCE=1",
		"--", "/usr/local/bin/px", "-p", "--print-json",
		"--session", "s-1", "--model", "anthropic/claude-sonnet-5", "--", "hello there",
	}
	assertArgsEqual(t, got, want)
}

func TestTurnRunArgs_YoloFlagIncludedWhenSet(t *testing.T) {
	spec := orchestrator.TurnSpec{SessionID: "s-1", Message: "hi", Yolo: true}
	got := turnRunArgs(testConfig(), "px-alpha", spec)
	found := false
	for _, a := range got {
		if a == "--yolo" {
			found = true
		}
	}
	if !found {
		t.Errorf("args = %v, want --yolo present", got)
	}
}

func TestTurnRunArgs_MessageAlwaysLast(t *testing.T) {
	spec := orchestrator.TurnSpec{SessionID: "s-1", Message: "-rm test"}
	got := turnRunArgs(testConfig(), "px-alpha", spec)
	if got[len(got)-1] != "-rm test" {
		t.Errorf("last arg = %q, want the literal message", got[len(got)-1])
	}
	if got[len(got)-2] != "--" {
		t.Errorf("second-to-last arg = %q, want \"--\" immediately before the message", got[len(got)-2])
	}
}

func TestSystemctlStopUnitByNameArgs(t *testing.T) {
	assertArgsEqual(t, systemctlStopUnitByNameArgs("px-turn-px-alpha"), []string{"stop", "px-turn-px-alpha.service"})
}
