package tui

import (
	"slices"
	"testing"
)

func TestDirtyTrackerFullPrecedence(t *testing.T) {
	d := newDirtyTracker()
	d.markScrollRows(1, 2)
	d.markFull()
	snap := d.consume()
	if !snap.full {
		t.Fatal("expected full snapshot")
	}
	if snap.any() != true {
		t.Fatal("full snapshot should be dirty")
	}
	// Second consume should be empty.
	if d.consume().any() {
		t.Fatal("expected clean tracker after consume")
	}
}

func TestDirtyTrackerPartialMerge(t *testing.T) {
	d := newDirtyTracker()
	d.markScrollRows(3)
	d.markScrollRows(3, 4)
	d.markInput()
	snap := d.consume()
	if snap.full {
		t.Fatal("unexpected full")
	}
	if !snap.input || !snap.cursor {
		t.Fatalf("input/cursor flags: input=%v cursor=%v", snap.input, snap.cursor)
	}
	got := append([]int(nil), snap.scroll...)
	slices.Sort(got)
	got = slices.Compact(got)
	want := []int{3, 4}
	if len(got) != len(want) {
		t.Fatalf("scroll rows = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("scroll rows = %v, want %v", got, want)
		}
	}
}

func TestDirtyTrackerMarkFullClearsOnConsume(t *testing.T) {
	d := newDirtyTracker()
	d.markStatus()
	d.markFull()
	snap := d.consume()
	if !snap.full || !snap.input || !snap.status {
		t.Fatalf("full snap should carry all regions: %+v", snap)
	}
}
