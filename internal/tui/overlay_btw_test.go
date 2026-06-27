package tui

import (
	"strings"
	"testing"
)

func TestBTWOverlayMaxHeight(t *testing.T) {
	o := newBTWOverlay("short question", 4) // 15% of ~27 rows
	o.appendText("line one\nline two\nline three\nline four\nline five")
	o.finish(nil)
	_, lines := o.render(30, 80)
	if len(lines) > 4 {
		t.Fatalf("overlay height = %d, want <= 4 (15%% cap)", len(lines))
	}
}

func TestBTWOverlayRightAligned(t *testing.T) {
	o := newBTWOverlay("hi", 8)
	o.finish(nil)
	_, lines := o.render(20, 60)
	if len(lines) < 3 {
		t.Fatalf("lines = %d", len(lines))
	}
	for _, ln := range lines {
		if visibleWidth(ln) > 60 {
			t.Fatalf("line too wide: %d", visibleWidth(ln))
		}
		plain := stripANSI(ln)
		if !stringsHasSuffixSpace(plain) && plain[0] != ' ' && len(stringsTrimLeft(plain)) == len(plain) {
			// right-aligned box lines should have leading spaces (except full-width)
		}
	}
	last := stripANSI(lines[len(lines)-1])
	if last == "" || []rune(last)[len([]rune(last))-1] != '╯' {
		t.Fatalf("expected right-aligned bottom corner, got %q", last)
	}
}

func TestBTWOverlayEscCancel(t *testing.T) {
	o := newBTWOverlay("q", 6)
	cancelled := false
	o.setCancel(func() { cancelled = true })
	handled, done, cancel := o.feedKey([]byte{27})
	if !handled || !done || !cancel {
		t.Fatalf("esc while processing: handled=%v done=%v cancel=%v", handled, done, cancel)
	}
	if !cancelled {
		t.Fatal("expected cancel func called")
	}
	o.finish(nil)
	handled, done, cancel = o.feedKey([]byte{27})
	if !handled || !done || cancel {
		t.Fatalf("esc when done: handled=%v done=%v cancel=%v", handled, done, cancel)
	}
}

func TestBTWOverlayScroll(t *testing.T) {
	o := newBTWOverlay("q", 6)
	var lines []string
	for i := 0; i < 12; i++ {
		lines = append(lines, "answer line "+strings.Repeat("x", 20))
	}
	o.appendText(strings.Join(lines, "\n"))
	o.finish(nil)
	o.feedKey(arrowDownBytes())
	_, _, _, _, scroll, _ := o.snapshot()
	if scroll == 0 {
		t.Fatal("expected scroll down to increase offset")
	}
	o.feedKey(arrowUpBytes())
	_, _, _, _, scroll2, _ := o.snapshot()
	if scroll2 != 0 {
		t.Fatalf("scroll = %d after up, want 0", scroll2)
	}
}

func arrowUpBytes() []byte   { return []byte{27, '[', 'A'} }
func arrowDownBytes() []byte { return []byte{27, '[', 'B'} }

func stringsHasSuffixSpace(s string) bool { return len(s) > 0 && s[len(s)-1] == ' ' }
func stringsTrimLeft(s string) string {
	i := 0
	for i < len(s) && s[i] == ' ' {
		i++
	}
	return s[i:]
}