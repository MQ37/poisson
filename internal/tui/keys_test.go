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

func TestDecodeKittyKeysCompat(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want []byte
	}{
		{"kitty arrow up", []byte{27, '[', '5', '7', '3', '5', '2', 'u'}, []byte{27, '[', 'A'}},
		{"kitty enter", []byte{27, '[', '5', '7', '3', '4', '5', 'u'}, []byte{'\r'}},
		{"legacy arrow", []byte{27, '[', 'A'}, []byte{27, '[', 'A'}},
	}
	for _, c := range cases {
		got := decodeKittyKeys(c.in)
		if string(got) != string(c.want) {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}