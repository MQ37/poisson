package tui

import "testing"

func keyArrowUp() Key   { return Key{Kind: KeyArrowUp} }
func keyArrowDown() Key { return Key{Kind: KeyArrowDown} }

func TestDecoderKittyArrowUp(t *testing.T) {
	var d Decoder
	keys := d.Push([]byte{27, '[', '5', '7', '3', '5', '2', 'u'})
	if len(keys) != 1 || keys[0].Kind != KeyArrowUp {
		t.Fatalf("keys=%v", keys)
	}
}

func TestDecoderBuffersIncompleteCSI(t *testing.T) {
	var d Decoder
	k1 := d.Push([]byte{27, '['})
	if len(k1) != 0 || len(d.pending) != 2 {
		t.Fatalf("first push: keys=%v pending=%q", k1, d.pending)
	}
	k2 := d.Push([]byte{'A'})
	if len(k2) != 1 || k2[0].Kind != KeyArrowUp {
		t.Fatalf("second push: keys=%v", k2)
	}
	if len(d.pending) != 0 {
		t.Fatalf("pending=%q", d.pending)
	}
}

func TestDecoderIncompleteCSINeverEmitsBracketRune(t *testing.T) {
	var d Decoder
	if keys := d.Push([]byte{27, '['}); len(keys) != 0 {
		t.Fatalf("expected no keys from partial CSI, got %v", keys)
	}
	for _, k := range d.Push([]byte{'5', '7', '3', '5', '2', 'u'}) {
		if k.Kind == KeyRune && k.Rune == '[' {
			t.Fatal("partial kitty CSI leaked '[' as text")
		}
	}
}

// TestDecoderSplitUTF8RuneAcrossPushes is the regression test for
// parsePlain silently dropping a multi-byte UTF-8 rune whose bytes arrive
// split across two Push calls (plausible over ssh/mosh with per-byte
// delivery, or any IME/CJK keystroke that just happens to straddle a read
// boundary) — utf8.DecodeRune on an incomplete lead sequence returns
// (RuneError, 1), indistinguishable from a genuinely invalid byte, so the
// byte used to get discarded as KeyUnknown instead of buffered.
func TestDecoderSplitUTF8RuneAcrossPushes(t *testing.T) {
	full := []byte("世") // U+4E16, 3-byte UTF-8 encoding
	var d Decoder
	k1 := d.Push(full[:1])
	if len(k1) != 0 {
		t.Fatalf("first (partial) push: keys=%v, want none", k1)
	}
	if len(d.pending) != 1 {
		t.Fatalf("pending=%q, want the 1 lead byte buffered", d.pending)
	}
	k2 := d.Push(full[1:])
	if len(k2) != 1 || k2[0].Kind != KeyRune || k2[0].Rune != '世' {
		t.Fatalf("second push: keys=%v, want one KeyRune('世')", k2)
	}
	if len(d.pending) != 0 {
		t.Fatalf("pending=%q, want empty after the rune completes", d.pending)
	}
}

// TestDecoderInvalidByteStillDiscardedAsUnknown verifies the fix for split
// UTF-8 runes (utf8.FullRune gating parsePlain's buffer-vs-discard
// decision) didn't turn a genuinely invalid stray byte into a permanent
// hang — it must still be consumed and reported as KeyUnknown immediately.
func TestDecoderInvalidByteStillDiscardedAsUnknown(t *testing.T) {
	var d Decoder
	keys := d.Push([]byte{0xFF, 'a'})
	if len(keys) != 1 || keys[0].Kind != KeyRune || keys[0].Rune != 'a' {
		t.Fatalf("keys=%v, want the invalid byte discarded and 'a' decoded normally", keys)
	}
	if len(d.pending) != 0 {
		t.Fatalf("pending=%q, want empty (invalid byte must not get stuck buffered)", d.pending)
	}
}

// TestDecoderLegacyShiftTab verifies the classic xterm Shift+Tab escape
// ("\x1b[Z", sent when the kitty keyboard protocol isn't active) decodes to
// KeyShiftTab, not KeyTab or KeyUnknown.
func TestDecoderLegacyShiftTab(t *testing.T) {
	var d Decoder
	keys := d.Push([]byte{27, '[', 'Z'})
	if len(keys) != 1 || keys[0].Kind != KeyShiftTab {
		t.Fatalf("keys=%v, want single KeyShiftTab", keys)
	}
}

// TestDecoderKittyShiftTab verifies the kitty keyboard protocol's Tab code
// (57346) with the shift modifier bit set decodes to KeyShiftTab, and
// without it decodes to plain KeyTab.
func TestDecoderKittyShiftTab(t *testing.T) {
	var d Decoder
	// kitty functional key 57346 (Tab), mods=2 (shift: mods-1=1, bit0 set).
	keys := d.Push([]byte("\x1b[57346;2u"))
	if len(keys) != 1 || keys[0].Kind != KeyShiftTab {
		t.Fatalf("keys=%v, want single KeyShiftTab", keys)
	}
	var d2 Decoder
	keys2 := d2.Push([]byte("\x1b[57346u"))
	if len(keys2) != 1 || keys2[0].Kind != KeyTab {
		t.Fatalf("keys=%v, want plain KeyTab with no modifier", keys2)
	}
}

// TestDecoderPlainTabUnaffected verifies a bare Tab byte (no kitty protocol,
// no CSI) still decodes to plain KeyTab — Shift+Tab decoding must not
// regress the far more common plain-Tab path.
func TestDecoderPlainTabUnaffected(t *testing.T) {
	var d Decoder
	keys := d.Push([]byte{'\t'})
	if len(keys) != 1 || keys[0].Kind != KeyTab {
		t.Fatalf("keys=%v, want single KeyTab", keys)
	}
}

func TestPickerFeedKeyKittyArrowDoesNotFilter(t *testing.T) {
	p := newPickerOverlay("Models", []pickerItem{
		{id: "a", label: "alpha"},
		{id: "b", label: "beta"},
	}, "a", nil)
	var d Decoder
	for _, k := range d.Push([]byte{27, '[', '5', '7', '3', '5', '3', 'u'}) {
		handled, _, _ := p.feedKey(k)
		if !handled {
			t.Fatal("kitty down not handled")
		}
	}
	if p.filter != "" {
		t.Fatalf("filter=%q want empty", p.filter)
	}
	if p.idx != 1 {
		t.Fatalf("idx=%d want 1", p.idx)
	}
}

func TestFeedPickerArrowKeysE2E(t *testing.T) {
	tui := newTestTUIHelper()
	p := newPickerOverlay("Models", []pickerItem{
		{id: "a", label: "alpha"},
		{id: "b", label: "beta"},
	}, "a", nil)
	tui.activeOverlay = p
	_, _ = tui.feedKey(keyArrowDown())
	if p.idx != 1 {
		t.Fatalf("down: idx=%d want 1", p.idx)
	}
	if p.filter != "" {
		t.Fatalf("filter=%q want empty", p.filter)
	}
	_, _ = tui.feedKey(keyArrowUp())
	if p.idx != 0 {
		t.Fatalf("up: idx=%d want 0", p.idx)
	}
}
