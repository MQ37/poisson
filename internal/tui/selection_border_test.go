package tui

import (
	"strings"
	"testing"
)

func TestRowContentBoundsPureBorderDropped(t *testing.T) {
	for _, s := range []string{"╰──────╯", "├───┼───┤", "  ─────  "} {
		_, _, drop := rowContentBounds([]rune(s))
		if !drop {
			t.Errorf("rowContentBounds(%q) drop=false, want true", s)
		}
	}
}

// TestRowContentBoundsBlankRowKept guards against a vacuous "every non-space
// rune is a border glyph" reading: a row of nothing but spaces (e.g. the
// blank line renderDiffLines inserts between edit hunks, tool_diff.go) has
// zero border glyphs and must NOT be dropped as if it were chrome — that
// would silently swallow real spacing from a copied diff.
func TestRowContentBoundsBlankRowKept(t *testing.T) {
	start, end, drop := rowContentBounds([]rune("        "))
	if drop {
		t.Fatal("all-space row with no border glyph must not be dropped")
	}
	if start != 0 || end != 8 {
		t.Fatalf("bounds = (%d,%d), want (0,8)", start, end)
	}
}

func TestRowContentBoundsFenceRowDropped(t *testing.T) {
	_, _, drop := rowContentBounds([]rune("╭─ python ──────╮"))
	if !drop {
		t.Fatal("fence label row should be dropped whole")
	}
}

func TestRowContentBoundsStripsSideBorders(t *testing.T) {
	runes := []rune("│ print(x) │")
	start, end, drop := rowContentBounds(runes)
	if drop {
		t.Fatal("content row should not be dropped")
	}
	if got := string(runes[start:end]); got != "print(x)" {
		t.Fatalf("content = %q, want %q", got, "print(x)")
	}
}

func TestRowContentBoundsOrdinaryRowUnaffected(t *testing.T) {
	runes := []rune("hello world")
	start, end, drop := rowContentBounds(runes)
	if drop || start != 0 || end != len(runes) {
		t.Fatalf("ordinary row bounds = (%d,%d,%v), want (0,%d,false)", start, end, drop, len(runes))
	}
}

func TestSelectedTextDropsCodeFenceBorders(t *testing.T) {
	s := newScrollback(1024)
	s.appendBlock(blockAssistant, "```python\nprint(x)\ny = 2\n```")
	wrapped, _ := s.layoutAll(80)
	got := s.selectedText(80, 0, 0, len(wrapped)-1, 999)
	want := "print(x)\ny = 2"
	if got != want {
		t.Fatalf("selectedText = %q, want %q", got, want)
	}
}

func TestSelectedTextDropsTableBorders(t *testing.T) {
	s := newScrollback(1024)
	s.appendBlock(blockAssistant, "| A | B |\n|---|---|\n| 1 | 2 |")
	wrapped, _ := s.layoutAll(80)
	got := s.selectedText(80, 0, 0, len(wrapped)-1, 999)
	for _, glyph := range []string{"│", "╭", "╮", "╰", "╯", "├", "┤", "┬", "┴", "┼"} {
		if strings.Contains(got, glyph) {
			t.Fatalf("selectedText = %q, must not contain border glyph %q", got, glyph)
		}
	}
	if !strings.Contains(got, "A") || !strings.Contains(got, "1") {
		t.Fatalf("selectedText = %q, want table cell content", got)
	}
}

func TestSelectedTextPartialLineInsideCodeBlockUnaffected(t *testing.T) {
	s := newScrollback(1024)
	s.appendBlock(blockAssistant, "```\nabcdefgh\n```")
	// Row 1 (0-indexed within this block) is "│ abcdefgh │" — select columns
	// 5..7, well inside the content, not touching either border edge.
	got := s.selectedText(80, 1, 5, 1, 7)
	if got != "def" {
		t.Fatalf("selectedText = %q, want %q (border-stripping must not fire on a mid-line clip)", got, "def")
	}
}

