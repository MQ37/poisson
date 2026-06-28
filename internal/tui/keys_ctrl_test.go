package tui

import "testing"

func TestDecoderKittyCtrlS(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
	}{
		{"c0 code", []byte{27, '[', '1', '9', ';', '5', 'u'}},
		{"letter s", []byte{27, '[', '1', '1', '5', ';', '5', 'u'}},
		{"plain xoff", []byte{19}},
	}
	for _, c := range cases {
		var d Decoder
		keys := d.Push(c.in)
		if len(keys) != 1 {
			t.Fatalf("%s: keys=%v want 1", c.name, keys)
		}
		if keys[0].Kind != KeyCtrl || keys[0].Byte != 19 {
			t.Fatalf("%s: got %+v want Ctrl+S (byte 19)", c.name, keys[0])
		}
	}
}

func TestFeedKeyCtrlSOpensSessionPicker(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)
	_, err := tui.feedKey(Key{Kind: KeyCtrl, Byte: 19})
	if err != nil {
		t.Fatal(err)
	}
	if tui.activeOverlay == nil {
		t.Fatal("expected session picker overlay")
	}
}