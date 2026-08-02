package tui

import (
	"fmt"
	"testing"
)

// TestScrollToSearchMatchLockedClampsAndCenters seeds a known total row
// count (100 single-row blocks at a content width wide enough to avoid
// wrapping) and checks scrollToSearchMatchLocked's centering/clamp
// arithmetic at both extremes and in the middle.
//
// With viewH=20 (convScrollRows, no pins) and 100 total rows:
//
//	max = 100 - 20 = 80
//	off(globalRow) = 100 - globalRow - viewH/2, clamped to [0, max]
func TestScrollToSearchMatchLockedClampsAndCenters(t *testing.T) {
	tui := newTestTUIHelper()
	const total = 100
	for i := 0; i < total; i++ {
		tui.scroll.appendRaw(styleSystem, fmt.Sprintf("line %d", i))
	}

	width := tui.contentWidth()
	wrapped, _ := tui.scroll.layoutAll(width)
	if len(wrapped) != total {
		t.Fatalf("test setup: got %d wrapped rows, want %d (lines must not wrap)", len(wrapped), total)
	}
	viewH := tui.convScrollRows()
	if viewH != 20 {
		t.Fatalf("test setup: convScrollRows = %d, want 20", viewH)
	}
	max := total - viewH

	cases := []struct {
		name      string
		globalRow int
		want      int
	}{
		{"top row clamps to max offset", 0, max},
		{"middle row centers", 50, total - 50 - viewH/2},
		{"last row clamps to zero", total - 1, 0},
	}
	for _, c := range cases {
		tui.mu.Lock()
		tui.scrollToSearchMatchLocked(c.globalRow)
		got := tui.scroll.scrollOffset
		tui.mu.Unlock()
		if got != c.want {
			t.Errorf("%s: globalRow=%d scrollOffset=%d, want %d", c.name, c.globalRow, got, c.want)
		}
	}
}
