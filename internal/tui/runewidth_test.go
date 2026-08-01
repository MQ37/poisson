package tui

import "testing"

// TestInRangesBinarySearch spot-checks the shared binary-search helper
// directly: boundary hits, misses, and a gap between two ranges.
func TestInRangesBinarySearch(t *testing.T) {
	ranges := []runeRange{{10, 20}, {30, 40}}
	cases := []struct {
		r    rune
		want bool
	}{
		{9, false},
		{10, true},  // lo boundary
		{15, true},  // middle
		{20, true},  // hi boundary
		{21, false}, // just past first range
		{25, false}, // gap between ranges
		{30, true},
		{40, true},
		{41, false},
	}
	for _, c := range cases {
		if got := inRanges(c.r, ranges); got != c.want {
			t.Errorf("inRanges(%d) = %v, want %v", c.r, got, c.want)
		}
	}
}

// TestRuneDisplayWidthCoversMajorBlocks is a broader sweep than
// wrapped_test.go's TestRuneDisplayWidth: one representative code point
// from every block in wideRanges/zeroWidthRanges, plus common ASCII/Latin
// controls, so the hand-maintained table's boundaries are each exercised
// at least once.
func TestRuneDisplayWidthCoversMajorBlocks(t *testing.T) {
	wide := []rune{
		0x1100,  // Hangul Jamo start
		0x3042,  // Hiragana A
		0x30A2,  // Katakana A
		0x4E2D,  // 中 (CJK Unified)
		0x9FFF,  // CJK Unified end
		0xAC00,  // Hangul Syllable start (가)
		0xD7A3,  // Hangul Syllable end
		0xFF21,  // Fullwidth Latin A
		0x1F600, // 😀 emoji
		0x1F680, // 🚀 emoji
		0x1FA00, // Extended-A pictograph start
		0x20000, // CJK Ext B start
	}
	for _, r := range wide {
		if got := runeDisplayWidth(r); got != 2 {
			t.Errorf("runeDisplayWidth(%#x) = %d, want 2 (wide)", r, got)
		}
	}

	zero := []rune{
		0x0301, // combining acute accent
		0x200B, // zero width space
		0x2060, // word joiner
		0xFE0F, // variation selector-16
		0xFEFF, // BOM
	}
	for _, r := range zero {
		if got := runeDisplayWidth(r); got != 0 {
			t.Errorf("runeDisplayWidth(%#x) = %d, want 0 (zero-width)", r, got)
		}
	}

	narrow := []rune{'a', 'Z', '0', ' ', '!', 0x00E9 /* é */, 0x0041}
	for _, r := range narrow {
		if got := runeDisplayWidth(r); got != 1 {
			t.Errorf("runeDisplayWidth(%#x) = %d, want 1 (narrow)", r, got)
		}
	}
}
