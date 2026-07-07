package tui

import (
	"strings"
	"testing"
)

func TestBTWOverlayFillsScrollRegionFullWidth(t *testing.T) {
	o := newBTWOverlay("short question")
	o.appendText("line one\nline two")
	o.finish(nil)
	_, lines := o.render(30, 80)
	// The panel fills the scroll region and every line spans the full width,
	// like the bash-approval overlay.
	if len(lines) != 30 {
		t.Fatalf("panel height = %d, want 30 (fills scroll region)", len(lines))
	}
	for i, ln := range lines {
		if w := visibleWidth(ln); w != 80 {
			t.Fatalf("line %d width = %d, want 80 (full width)", i, w)
		}
	}
}

func TestBTWOverlayTitleAndFooter(t *testing.T) {
	o := newBTWOverlay("hi")
	o.finish(nil)
	anchor, lines := o.render(24, 80)
	if anchor != 1 {
		t.Fatalf("anchor = %d, want 1", anchor)
	}
	if !strings.Contains(stripANSI(lines[0]), "btw") {
		t.Fatalf("expected 'btw' title, got %q", stripANSI(lines[0]))
	}
	if !strings.Contains(stripANSI(lines[len(lines)-1]), "Esc") {
		t.Fatalf("expected Esc in footer, got %q", stripANSI(lines[len(lines)-1]))
	}
}

func TestBTWOverlayEscCancel(t *testing.T) {
	o := newBTWOverlay("q")
	cancelled := false
	o.setCancel(func() { cancelled = true })
	handled, done, cancel := o.feedKey(Key{Kind: KeyEscape})
	if !handled || !done || !cancel {
		t.Fatalf("esc while processing: handled=%v done=%v cancel=%v", handled, done, cancel)
	}
	if !cancelled {
		t.Fatal("expected cancel func called")
	}
	o.finish(nil)
	handled, done, cancel = o.feedKey(Key{Kind: KeyEscape})
	if !handled || !done || cancel {
		t.Fatalf("esc when done: handled=%v done=%v cancel=%v", handled, done, cancel)
	}
}

func TestBTWOverlayScroll(t *testing.T) {
	o := newBTWOverlay("q")
	var lines []string
	for i := 0; i < 40; i++ {
		lines = append(lines, "answer line "+strings.Repeat("x", 20))
	}
	o.appendText(strings.Join(lines, "\n"))
	o.finish(nil)
	// Render once so the overlay knows its scroll bounds.
	o.render(12, 80)
	o.feedKey(keyArrowDown())
	if _, _, _, _, scroll := o.snapshot(); scroll == 0 {
		t.Fatal("expected scroll down to increase offset")
	}
	o.feedKey(keyArrowUp())
	if _, _, _, _, scroll := o.snapshot(); scroll != 0 {
		t.Fatalf("scroll = %d after up, want 0", scroll)
	}
}
