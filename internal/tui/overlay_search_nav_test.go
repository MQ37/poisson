package tui

import "testing"

// TestSearchOverlayNextWrapsBackward: stepping before the first match wraps
// around to the LAST match index, not to 0 or a negative/out-of-range value.
func TestSearchOverlayNextWrapsBackward(t *testing.T) {
	s := &searchOverlay{matches: []int{5, 9, 20}, cur: 0}
	s.next(-1)
	if s.cur != 2 {
		t.Fatalf("cur = %d, want 2 (last index)", s.cur)
	}
	if got := s.currentGlobalRow(); got != 20 {
		t.Fatalf("currentGlobalRow = %d, want 20", got)
	}
}

// TestSearchOverlayNextWrapsForward: stepping past the last match wraps
// around to the FIRST match index.
func TestSearchOverlayNextWrapsForward(t *testing.T) {
	s := &searchOverlay{matches: []int{5, 9, 20}, cur: 2}
	s.next(1)
	if s.cur != 0 {
		t.Fatalf("cur = %d, want 0 (first index)", s.cur)
	}
	if got := s.currentGlobalRow(); got != 5 {
		t.Fatalf("currentGlobalRow = %d, want 5", got)
	}
}

// TestSearchOverlayCurrentGlobalRowSentinel: an out-of-bounds cur or an
// empty matches slice must return the -1 sentinel, not panic.
func TestSearchOverlayCurrentGlobalRowSentinel(t *testing.T) {
	s := &searchOverlay{matches: nil, cur: 0}
	if got := s.currentGlobalRow(); got != -1 {
		t.Fatalf("empty matches: currentGlobalRow = %d, want -1", got)
	}

	s2 := &searchOverlay{matches: []int{5, 9, 20}, cur: 7}
	if got := s2.currentGlobalRow(); got != -1 {
		t.Fatalf("out-of-bounds cur: currentGlobalRow = %d, want -1", got)
	}

	s3 := &searchOverlay{matches: []int{5, 9, 20}, cur: -1}
	if got := s3.currentGlobalRow(); got != -1 {
		t.Fatalf("negative cur: currentGlobalRow = %d, want -1", got)
	}
}

// TestSearchOverlayMatchRowsIsSnapshot: matchRows returns a copy, not the
// live backing slice — mutating the result must not affect s.matches.
func TestSearchOverlayMatchRowsIsSnapshot(t *testing.T) {
	s := &searchOverlay{matches: []int{5, 9, 20}}
	rows := s.matchRows()
	if len(rows) != 3 || rows[0] != 5 || rows[1] != 9 || rows[2] != 20 {
		t.Fatalf("matchRows = %v", rows)
	}
	rows[0] = 999
	if s.matches[0] != 5 {
		t.Fatal("matchRows leaked a mutable view of the internal slice")
	}
}
