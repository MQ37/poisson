package tui

import "testing"

// TestParseMouseWheelScrollValidSequence: a single valid SGR wheel-up
// sequence alone is accepted with the correct delta.
func TestParseMouseWheelScrollValidSequence(t *testing.T) {
	delta, ok := parseMouseWheelScroll([]byte("\x1b[<64;1;1M"))
	if !ok || delta != 3 {
		t.Fatalf("delta=%d ok=%v, want 3 true", delta, ok)
	}
}

func TestParseMouseWheelScrollWheelDown(t *testing.T) {
	delta, ok := parseMouseWheelScroll([]byte("\x1b[<65;1;1M"))
	if !ok || delta != -3 {
		t.Fatalf("delta=%d ok=%v, want -3 true", delta, ok)
	}
}

// TestParseMouseWheelScrollRejectsTrailingByte: the same sequence with one
// extra unrelated byte appended must be rejected — this proves the wheel
// parser only fires when the whole chunk is purely the wheel event, not
// merely when a wheel event is present somewhere within it.
func TestParseMouseWheelScrollRejectsTrailingByte(t *testing.T) {
	delta, ok := parseMouseWheelScroll([]byte("\x1b[<64;1;1Mx"))
	if ok {
		t.Fatalf("expected rejection of trailing extra byte, got delta=%d ok=%v", delta, ok)
	}
}

// TestParseMouseWheelScrollRejectsLeadingByte: same proof, leading garbage.
func TestParseMouseWheelScrollRejectsLeadingByte(t *testing.T) {
	delta, ok := parseMouseWheelScroll([]byte("x\x1b[<64;1;1M"))
	if ok {
		t.Fatalf("expected rejection of leading extra byte, got delta=%d ok=%v", delta, ok)
	}
}
