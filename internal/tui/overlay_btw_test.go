package tui

import (
	"strings"
	"testing"
)

func TestBTWOverlayMaxHeight(t *testing.T) {
	o := newBTWOverlay("short question", 12)
	o.appendText("line one\nline two\nline three\nline four\nline five")
	o.finish(nil)
	_, lines := o.render(30, 80)
	if len(lines) > 12 {
		t.Fatalf("overlay height = %d, want <= 12", len(lines))
	}
}

func TestBTWOverlayFullWidthLeftAligned(t *testing.T) {
	o := newBTWOverlay("hi", 10)
	o.finish(nil)
	anchor, lines := o.render(24, 80)
	if len(lines) < 3 {
		t.Fatalf("lines = %d", len(lines))
	}
	if anchor < 8 {
		t.Fatalf("expected lower placement, anchor=%d", anchor)
	}
	top := stripANSI(lines[0])
	if !strings.HasPrefix(top, "╭") {
		t.Fatalf("expected left-aligned top border, got %q", top)
	}
	if strings.HasPrefix(top, " ") {
		t.Fatalf("top border should not be right-padded: %q", top)
	}
	last := stripANSI(lines[len(lines)-1])
	if !strings.HasPrefix(last, "╰") {
		t.Fatalf("expected left-aligned bottom border, got %q", last)
	}
	for _, ln := range lines {
		if visibleWidth(ln) > 80 {
			t.Fatalf("line too wide: %d", visibleWidth(ln))
		}
	}
}

func TestBTWOverlayEscCancel(t *testing.T) {
	o := newBTWOverlay("q", 6)
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
	o := newBTWOverlay("q", 8)
	var lines []string
	for i := 0; i < 12; i++ {
		lines = append(lines, "answer line "+strings.Repeat("x", 20))
	}
	o.appendText(strings.Join(lines, "\n"))
	o.finish(nil)
	o.feedKey(keyArrowDown())
	_, _, _, _, scroll, _ := o.snapshot()
	if scroll == 0 {
		t.Fatal("expected scroll down to increase offset")
	}
	o.feedKey(keyArrowUp())
	_, _, _, _, scroll2, _ := o.snapshot()
	if scroll2 != 0 {
		t.Fatalf("scroll = %d after up, want 0", scroll2)
	}
}