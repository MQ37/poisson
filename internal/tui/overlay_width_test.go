package tui

import (
	"strings"
	"testing"
)

// longModelID is a real custom llama.cpp GGUF name (78 columns) — the case that
// motivated dropping the 72-column cap on list modals.
const longModelID = "DavidAU/Qwen3.6-27B-Fable-Fusion-711-Uncensored-Heretic-NM-DAU-NEO-MAX-MTP-GGUF"

// TestBoxedListUsesFullTerminalWidth locks in the fix: a list modal spans the
// terminal instead of a fixed 72 columns, so long rows are not truncated while
// most of a wide terminal sits empty.
func TestBoxedListUsesFullTerminalWidth(t *testing.T) {
	const cols = 120
	_, lines := renderBoxedList("Models (llamacpp)", "", []string{"  " + longModelID}, 24, cols, "")

	for i, ln := range lines {
		if got := visibleWidth(ln); got != cols {
			t.Fatalf("line %d width = %d, want %d (full terminal)", i, got, cols)
		}
	}
	if !strings.Contains(stripANSI(strings.Join(lines, "\n")), longModelID) {
		t.Errorf("long model id was truncated; box body:\n%s", stripANSI(strings.Join(lines, "\n")))
	}
}

// TestModelPickerShowsLongIDInFull is the same guarantee one level up, through
// the picker overlay a user actually opens with /model.
func TestModelPickerShowsLongIDInFull(t *testing.T) {
	p := newPickerOverlay("Models (llamacpp)", []pickerItem{
		{id: longModelID, label: longModelID, hint: "128,000 ctx"},
		{id: "short", label: "short"},
	}, longModelID, nil)

	_, lines := p.render(24, 120)
	if !strings.Contains(stripANSI(strings.Join(lines, "\n")), longModelID) {
		t.Errorf("model picker truncated %q:\n%s", longModelID, stripANSI(strings.Join(lines, "\n")))
	}
}

// TestBoxedListNarrowTerminalStillFits guards the other end: an 80-column
// terminal must not produce lines wider than itself (which would wrap and
// smear the box across two rows).
func TestBoxedListNarrowTerminalStillFits(t *testing.T) {
	const cols = 80
	_, lines := renderBoxedList("Models", "", []string{"  " + longModelID}, 24, cols, "")
	for i, ln := range lines {
		if got := visibleWidth(ln); got > cols {
			t.Fatalf("line %d width = %d, want <= %d", i, got, cols)
		}
	}
}
