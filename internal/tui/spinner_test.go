package tui

import "testing"

func TestSpinnerCharCycles(t *testing.T) {
	if spinnerChar(0) != "⠋" {
		t.Fatalf("frame 0 = %q", spinnerChar(0))
	}
	if spinnerChar(len(spinnerFrames)) != spinnerChar(0) {
		t.Fatalf("frame should wrap")
	}
}

func TestNeedsSpinner(t *testing.T) {
	if needsSpinner(false, 0, false) {
		t.Fatal("idle should not need spinner")
	}
	if !needsSpinner(true, 0, false) {
		t.Fatal("thinking should need spinner")
	}
	if !needsSpinner(false, 1, false) {
		t.Fatal("active tools should need spinner")
	}
	if !needsSpinner(false, 0, true) {
		t.Fatal("compacting should need spinner")
	}
}
