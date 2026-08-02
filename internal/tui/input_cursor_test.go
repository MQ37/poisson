package tui

import "testing"

func TestEditorMoveLeftWithinRow(t *testing.T) {
	e := newEditor()
	e.lines = []string{"abc"}
	e.row, e.col = 0, 2
	e.moveLeft()
	if e.col != 1 {
		t.Fatalf("col = %d, want 1", e.col)
	}
}

// TestEditorMoveLeftWrapsToPreviousLogicalRow: col==0 with row>0 must move to
// the end of the previous logical line, not stay put or panic.
func TestEditorMoveLeftWrapsToPreviousLogicalRow(t *testing.T) {
	e := newEditor()
	e.lines = []string{"ab", "cd"}
	e.row, e.col = 1, 0
	e.moveLeft()
	if e.row != 0 || e.col != 2 {
		t.Fatalf("row=%d col=%d, want row=0 col=2 (end of \"ab\")", e.row, e.col)
	}
}

func TestEditorMoveLeftAtOrigin(t *testing.T) {
	e := newEditor()
	e.row, e.col = 0, 0
	e.moveLeft() // no previous row -> must stay put, not panic
	if e.row != 0 || e.col != 0 {
		t.Fatalf("row=%d col=%d, want 0,0", e.row, e.col)
	}
}

// TestEditorMoveHomeEndScreenUsesScreenBoundary confirms moveHomeScreen and
// moveEndScreen land on the wrapped SCREEN-line boundary, not the logical
// line's start/end, when a single logical line spans multiple screen rows.
func TestEditorMoveHomeEndScreenUsesScreenBoundary(t *testing.T) {
	e := newEditor()
	width := 5
	e.lines = []string{"abcdefghij"} // 10 runes -> wraps to "abcde" | "fghij" at width 5
	e.row, e.col = 0, 7              // sits in the second screen line ("fghij", offset 2)

	e.moveHomeScreen(width)
	if e.row != 0 || e.col != 5 {
		t.Fatalf("moveHomeScreen: row=%d col=%d, want row=0 col=5 (screen-line start, not logical col 0)", e.row, e.col)
	}

	e.row, e.col = 0, 7
	e.moveEndScreen(width)
	if e.row != 0 || e.col != 10 {
		t.Fatalf("moveEndScreen: row=%d col=%d, want row=0 col=10 (screen-line end, clamped to line length)", e.row, e.col)
	}
}

// TestEditorMoveHomeEndScreenFirstScreenLine: cursor already on the first
// screen line of a wrapped logical row.
func TestEditorMoveHomeEndScreenFirstScreenLine(t *testing.T) {
	e := newEditor()
	width := 5
	e.lines = []string{"abcdefghij"}
	e.row, e.col = 0, 3

	e.moveHomeScreen(width)
	if e.col != 0 {
		t.Fatalf("moveHomeScreen col = %d, want 0", e.col)
	}

	e.row, e.col = 0, 3
	e.moveEndScreen(width)
	if e.col != 5 {
		t.Fatalf("moveEndScreen col = %d, want 5 (first screen line end, not full logical line end)", e.col)
	}
}

// TestEditorApplyKeyCtrlALiveDispatch sends a real Key{Kind:KeyCtrl,Byte:1}
// (Ctrl+A) through editor.applyKey — the same entry point production uses
// (TUI.processEditorKey -> t.editor.applyKey(k) -> case KeyCtrl ->
// e.applyCtrlKey, per key_dispatch.go) rather than calling applyCtrlKey
// directly, since that live path had zero coverage even though the parallel
// legacy byte-oriented path (editor.feed) is tested.
func TestEditorApplyKeyCtrlALiveDispatch(t *testing.T) {
	e := newEditor()
	e.wrapWidth = 5
	e.lines = []string{"abcdefghij"}
	e.row, e.col = 0, 7

	submitted, quit := e.applyKey(Key{Kind: KeyCtrl, Byte: 1})
	if submitted != "" || quit {
		t.Fatalf("unexpected submit/quit: %q %v", submitted, quit)
	}
	if e.row != 0 || e.col != 5 {
		t.Fatalf("Ctrl+A via applyKey: row=%d col=%d, want row=0 col=5", e.row, e.col)
	}
}

// TestEditorApplyKeyCtrlELiveDispatch is the Ctrl+E counterpart.
func TestEditorApplyKeyCtrlELiveDispatch(t *testing.T) {
	e := newEditor()
	e.wrapWidth = 5
	e.lines = []string{"abcdefghij"}
	e.row, e.col = 0, 2

	submitted, quit := e.applyKey(Key{Kind: KeyCtrl, Byte: 5})
	if submitted != "" || quit {
		t.Fatalf("unexpected submit/quit: %q %v", submitted, quit)
	}
	if e.row != 0 || e.col != 5 {
		t.Fatalf("Ctrl+E via applyKey: row=%d col=%d, want row=0 col=5", e.row, e.col)
	}
}

// TestRuneSubstringMultiByte confirms a multi-byte rune ('é', 2 bytes in
// UTF-8) is never split — this is the same bug class as the earlier
// CJK-width / UTF-8 rune-split fixes.
func TestRuneSubstringMultiByte(t *testing.T) {
	got := runeSubstring("héllo", 1, 4)
	if got != "éll" {
		t.Fatalf("runeSubstring(%q,1,4) = %q, want %q", "héllo", got, "éll")
	}
}

func TestRuneSubstringBoundsClamped(t *testing.T) {
	if got := runeSubstring("abc", -5, 100); got != "abc" {
		t.Fatalf("out-of-range bounds should clamp: got %q", got)
	}
	if got := runeSubstring("", 0, 0); got != "" {
		t.Fatalf("empty string: got %q", got)
	}
}
