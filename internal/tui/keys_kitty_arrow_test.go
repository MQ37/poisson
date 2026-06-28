package tui

import "testing"

func TestDecoderKittyDisambiguatedArrows(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want KeyKind
	}{
		{"legacy A", []byte{27, '[', 'A'}, KeyArrowUp},
		{"disambig 1A", []byte{27, '[', '1', 'A'}, KeyArrowUp},
		{"disambig 1;1A", []byte{27, '[', '1', ';', '1', 'A'}, KeyArrowUp},
		{"disambig 1;2A shift", []byte{27, '[', '1', ';', '2', 'A'}, KeyShiftArrowUp},
		{"kitty pua 2 field", []byte{27, '[', '5', '7', '3', '5', '2', ';', '1', 'u'}, KeyArrowUp},
		{"kitty pua colon press", []byte{27, '[', '5', '7', '3', '5', '2', ';', '1', ':', '1', 'u'}, KeyArrowUp},
		{"kitty pua colon down", []byte{27, '[', '5', '7', '3', '5', '3', ';', '1', ':', '1', 'u'}, KeyArrowDown},
		{"kitty pua semicolon press", []byte{27, '[', '5', '7', '3', '5', '2', ';', '1', ';', '1', 'u'}, KeyArrowUp},
		{"disambig colon event", []byte{27, '[', '1', ';', '1', ':', '1', 'A'}, KeyArrowUp},
		{"SS3 up", []byte{27, 'O', 'A'}, KeyArrowUp},
	}
	for _, c := range cases {
		var d Decoder
		keys := d.Push(c.in)
		if len(keys) != 1 {
			t.Fatalf("%s: keys=%v want 1", c.name, keys)
		}
		if keys[0].Kind != c.want {
			t.Fatalf("%s: got %v want %v", c.name, keys[0].Kind, c.want)
		}
	}
}

func TestDecoderKittyReleaseSwallowed(t *testing.T) {
	for _, seq := range [][]byte{
		{27, '[', '5', '7', '3', '5', '2', ';', '1', ';', '3', 'u'},
		{27, '[', '5', '7', '3', '5', '2', ';', '1', ':', '3', 'u'},
	} {
		var d Decoder
		if keys := d.Push(seq); len(keys) != 0 {
			t.Fatalf("release should emit no keys, got %v for %q", keys, seq)
		}
	}
}

func TestFeedKeyPickerDisambiguatedArrow(t *testing.T) {
	tui := newTestTUIHelper()
	p := newPickerOverlay("Models", []pickerItem{
		{id: "a", label: "alpha"},
		{id: "b", label: "beta"},
	}, "a", nil)
	tui.activeOverlay = p
	var d Decoder
	for _, k := range d.Push([]byte{27, '[', '1', 'B'}) {
		if !tui.handleKeyOverlay(k) {
			t.Fatalf("1B not handled: %v", k)
		}
	}
	if p.idx != 1 {
		t.Fatalf("idx=%d want 1", p.idx)
	}
}

func TestFeedKeyPickerKittyColonArrow(t *testing.T) {
	tui := newTestTUIHelper()
	p := newPickerOverlay("Models", []pickerItem{
		{id: "a", label: "alpha"},
		{id: "b", label: "beta"},
	}, "a", nil)
	tui.activeOverlay = p
	_, err := tui.feed([]byte{27, '[', '5', '7', '3', '5', '3', ';', '1', ':', '1', 'u'})
	if err != nil {
		t.Fatal(err)
	}
	if p.idx != 1 {
		t.Fatalf("idx=%d want 1", p.idx)
	}
}