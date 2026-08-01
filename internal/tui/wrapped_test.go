package tui

import "testing"

// TestRuneDisplayWidth spot-checks the CJK/emoji-vs-ASCII width split the
// rest of this file's wrap/cursor math depends on.
func TestRuneDisplayWidth(t *testing.T) {
	cases := []struct {
		r    rune
		want int
	}{
		{'a', 1},
		{'1', 1},
		{' ', 1},
		{'世', 2}, // CJK Unified Ideograph
		{'界', 2},
		{'あ', 2}, // Hiragana
		{'가', 2}, // Hangul
	}
	for _, c := range cases {
		if got := runeDisplayWidth(c.r); got != c.want {
			t.Errorf("runeDisplayWidth(%q) = %d, want %d", c.r, got, c.want)
		}
	}
}

// TestWrapOneCJKWrapsAtRealTerminalWidth is the direct regression test for
// the wide-CJK-rune-counted-as-1-column bug: 40 CJK runes at width=40 must
// wrap into 2 rows (they need 80 real terminal columns), not the 1 row a
// naive rune-count would compute.
func TestWrapOneCJKWrapsAtRealTerminalWidth(t *testing.T) {
	line := ""
	for i := 0; i < 40; i++ {
		line += "世"
	}
	chunks := wrapOne(line, 40)
	if len(chunks) != 2 {
		t.Fatalf("wrapOne(40 CJK runes, width=40) = %d chunks, want 2", len(chunks))
	}
	// Each row holds at most 20 CJK runes (20*2=40 columns).
	for i, c := range chunks {
		w := stringDisplayWidth(c)
		if w > 40 {
			t.Errorf("chunk %d display width = %d, want <= 40", i, w)
		}
	}
	// No rune lost or duplicated across the wrap.
	total := 0
	for _, c := range chunks {
		total += len([]rune(c))
	}
	if total != 40 {
		t.Errorf("total runes across chunks = %d, want 40", total)
	}
}

// TestWrapOneNeverSplitsAWideRune verifies a wide rune landing exactly at
// the last available column of a row moves whole to the next row instead
// of straddling (which isn't representable — a rune is atomic).
func TestWrapOneNeverSplitsAWideRune(t *testing.T) {
	// width=5: "aaaa" (4 cols) + "世" (2 cols) would total 6 > 5, so 世
	// must move to the next chunk entirely, not get clipped.
	chunks := wrapOne("aaaa世b", 5)
	if len(chunks) != 2 {
		t.Fatalf("chunks = %v, want 2", chunks)
	}
	if chunks[0] != "aaaa" {
		t.Errorf("chunks[0] = %q, want \"aaaa\" (世 pushed to next row whole)", chunks[0])
	}
	if chunks[1] != "世b" {
		t.Errorf("chunks[1] = %q, want \"世b\"", chunks[1])
	}
}

func TestVisualLineCountMatchesWrapOne(t *testing.T) {
	line := "世界世界世界世界世界" // 10 CJK runes = 20 columns
	for _, width := range []int{5, 10, 20, 21, 40} {
		got := visualLineCount(line, width)
		want := len(wrapOne(line, width))
		if got != want {
			t.Errorf("width=%d: visualLineCount = %d, want %d (== len(wrapOne(...)))", width, got, want)
		}
	}
}

// TestScreenCursorCJKColumnNotRuneIndex is the end-to-end regression test:
// with a CJK-heavy line, the cursor's screen column must reflect real
// display columns (2 per CJK rune), not a 1:1 rune count.
func TestScreenCursorCJKColumnNotRuneIndex(t *testing.T) {
	e := &editor{lines: []string{"世界hi"}, row: 0, col: 2} // cursor after "世界", before "hi"
	row, col := screenCursor(e, 80)
	if row != 0 {
		t.Fatalf("row = %d, want 0", row)
	}
	if col != 4 { // 世+界 = 2*2 = 4 display columns
		t.Errorf("col = %d, want 4 (2 CJK runes * 2 columns each, not 2 runes)", col)
	}
}

// TestScreenCursorWrapsIntoNextRowForCJK verifies a CJK line long enough to
// wrap places the cursor on the correct wrapped row, using real column
// accounting rather than rune counts.
func TestScreenCursorWrapsIntoNextRowForCJK(t *testing.T) {
	line := ""
	for i := 0; i < 20; i++ {
		line += "世" // 20 runes = 40 columns
	}
	e := &editor{lines: []string{line}, row: 0, col: 15} // 15th rune = 30 columns in
	row, col := screenCursor(e, 20)                      // width=20 columns -> 10 runes per row
	if row != 1 {
		t.Fatalf("row = %d, want 1 (rune 15 is on the 2nd wrapped row of 10-rune rows)", row)
	}
	if col != 10 { // (15-10)*2 = 10 columns into row 2
		t.Errorf("col = %d, want 10", col)
	}
}

// TestScreenToLogicalRoundTripsThroughScreenCursorForCJK verifies the two
// directions stay inverses of each other for CJK content at genuine rune
// boundaries (even columns: 0,2,4,6,8 — each CJK rune is 2 columns wide).
// An odd (mid-glyph) column has no single correct answer — there's no rune
// that starts there — so only the boundary columns get an exact-round-trip
// assertion; odd columns are checked only for staying in bounds.
func TestScreenToLogicalRoundTripsThroughScreenCursorForCJK(t *testing.T) {
	line := "世界世界" // 4 CJK runes = 8 columns
	for _, visualCol := range []int{0, 1, 2, 3, 4, 5, 6, 7, 8} {
		row, col := screenToLogical(&editor{lines: []string{line}}, 80, 0, visualCol)
		if row != 0 {
			t.Fatalf("visualCol=%d: row = %d, want 0", visualCol, row)
		}
		if col < 0 || col > 4 {
			t.Fatalf("visualCol=%d: col = %d, out of range [0,4]", visualCol, col)
		}
		if visualCol%2 != 0 {
			continue // mid-glyph column: no single correct rune index
		}
		_, backCol := screenCursor(&editor{lines: []string{line}, col: col}, 80)
		if backCol != visualCol {
			t.Errorf("visualCol=%d (rune boundary) -> col=%d -> screenCursor col=%d, want exact round trip to %d", visualCol, col, backCol, visualCol)
		}
	}
}

func TestVisibleWidthCountsCJKAsTwoColumns(t *testing.T) {
	if got := visibleWidth("世界"); got != 4 {
		t.Errorf("visibleWidth(2 CJK runes) = %d, want 4", got)
	}
	if got := visibleWidth("ab"); got != 2 {
		t.Errorf("visibleWidth(ab) = %d, want 2", got)
	}
	if got := visibleWidth("a世b"); got != 4 {
		t.Errorf("visibleWidth(a世b) = %d, want 4", got)
	}
}

func TestTruncateToWidthNeverSplitsAWideRune(t *testing.T) {
	// "世界世" is 6 columns; width=5 must drop the last rune entirely and
	// add the ellipsis (2 full runes = 4 columns + 1-column ellipsis = 5,
	// fits exactly), never emit a truncated/split glyph.
	got := truncateToWidth("世界世", 5)
	if visibleWidth(got) > 5 {
		t.Errorf("truncateToWidth result %q has display width %d, want <= 5", got, visibleWidth(got))
	}
	if got != "世界…" {
		t.Errorf("truncateToWidth(世界世, 5) = %q, want \"世界…\" (2 full runes + ellipsis fits exactly in 5 columns)", got)
	}

	// width=4: only 1 full CJK rune (2 cols) + ellipsis (1 col) = 3 fits;
	// a 2nd rune would need 2 more columns, overshooting the 3-column
	// budget left for real content — must drop to 1 rune, not split the
	// 2nd one in half.
	got = truncateToWidth("世界世", 4)
	if visibleWidth(got) > 4 {
		t.Errorf("truncateToWidth(世界世, 4) = %q, display width %d, want <= 4", got, visibleWidth(got))
	}
	if got != "世…" {
		t.Errorf("truncateToWidth(世界世, 4) = %q, want \"世…\"", got)
	}
}
