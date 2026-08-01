package tui

import "sort"

// runeRange is an inclusive Unicode code point range.
type runeRange struct{ lo, hi rune }

// inRanges reports whether r falls in any of ranges, which must be sorted
// ascending by lo and non-overlapping. Binary search over ~30-90 entries is
// effectively free next to the string/terminal I/O this feeds into.
func inRanges(r rune, ranges []runeRange) bool {
	i := sort.Search(len(ranges), func(i int) bool { return ranges[i].hi >= r })
	return i < len(ranges) && ranges[i].lo <= r
}

// wideRanges are code points a terminal renders 2 columns wide: CJK
// Unified Ideographs (and their extensions/compatibility blocks), Hangul,
// Hiragana/Katakana, fullwidth forms, and the common emoji blocks. Not a
// byte-perfect reproduction of Unicode's East Asian Width property (which
// runs to hundreds of fine-grained sub-ranges and an "Ambiguous" category
// terminals themselves disagree on) — a hand-maintained table covering the
// blocks that actually appear in real terminal input/output, in exchange
// for zero external dependencies. Source: the well-known, stable public
// Unicode block boundaries (unicode.org/Public/UNIDATA/Blocks.txt), not
// copied from any third-party width library.
var wideRanges = []runeRange{
	{0x1100, 0x115F}, // Hangul Jamo
	{0x2E80, 0x303E}, // CJK Radicals Supplement, Kangxi Radicals, CJK Symbols and Punctuation
	{0x3041, 0x33FF}, // Hiragana, Katakana, Bopomofo, Hangul Compat Jamo, Kanbun, CJK Strokes/Compat
	{0x3400, 0x4DBF}, // CJK Unified Ideographs Extension A
	{0x4E00, 0x9FFF}, // CJK Unified Ideographs
	{0xA000, 0xA4CF}, // Yi Syllables, Yi Radicals
	{0xAC00, 0xD7A3}, // Hangul Syllables
	{0xF900, 0xFAFF}, // CJK Compatibility Ideographs
	{0xFE30, 0xFE4F}, // CJK Compatibility Forms
	{0xFF00, 0xFF60}, // Fullwidth Forms
	{0xFFE0, 0xFFE6}, // Fullwidth Signs
	{0x1F300, 0x1F64F}, // Misc Symbols and Pictographs, Emoticons
	{0x1F680, 0x1F6FF}, // Transport and Map Symbols
	{0x1F900, 0x1F9FF}, // Supplemental Symbols and Pictographs
	{0x1FA00, 0x1FAFF}, // Symbols and Pictographs Extended-A
	{0x20000, 0x3FFFD}, // CJK Unified Ideographs Extensions B-G (supplementary planes)
}

// zeroWidthRanges are code points a terminal renders with no column of
// their own: combining marks, variation selectors, joiners, and the BOM.
// Covers the ranges that actually appear in practice (combining accents,
// emoji variation selectors, ZWJ) rather than every Unicode combining-class
// code point.
var zeroWidthRanges = []runeRange{
	{0x0300, 0x036F}, // Combining Diacritical Marks
	{0x200B, 0x200F}, // Zero Width Space/Joiner/Non-Joiner, marks
	{0x202A, 0x202E}, // Directional formatting
	{0x2060, 0x2064}, // Word joiner and friends
	{0xFE00, 0xFE0F}, // Variation Selectors
	{0xFEFF, 0xFEFF}, // BOM / zero width no-break space
}

// runeDisplayWidth is the terminal column width of r: 0 for zero-width
// combining marks, 1 for ordinary/narrow runes, 2 for wide runes (CJK
// Unified Ideographs, most emoji, fullwidth forms). Every wrap/cursor
// calculation in wrapped.go — and visibleWidth/truncateToWidth in
// scrollback.go — is defined in terms of display columns, not rune counts:
// a naive 1-rune=1-column assumption under-counts any wide rune by half,
// letting the real terminal auto-wrap mid-row on content this code still
// believes fits, corrupting every subsequent absolute-cursor-addressed
// write on screen for the rest of that frame.
func runeDisplayWidth(r rune) int {
	if r < 0x0300 {
		// Below the lowest entry in either table (zeroWidthRanges starts
		// at 0x0300) — ASCII and most of Latin-1 are always narrow, and
		// this skips both lookups for the overwhelmingly common case.
		return 1
	}
	if inRanges(r, zeroWidthRanges) {
		return 0
	}
	if inRanges(r, wideRanges) {
		return 2
	}
	return 1
}
