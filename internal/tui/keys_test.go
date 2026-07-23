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
