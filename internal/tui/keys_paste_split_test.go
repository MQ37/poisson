package tui

import "testing"

// TestDecoderPasteSplitAcrossPushCalls: every existing paste test sends the
// bracketed-paste start+end markers in a single Push call. This confirms the
// gap: a paste START marker with partial text and NO end marker in one Push
// call must emit zero keys and leave the decoder mid-paste; a second Push
// call completing it (remaining text + end marker) must then emit exactly
// one KeyPaste with the full concatenated text.
func TestDecoderPasteSplitAcrossPushCalls(t *testing.T) {
	var d Decoder

	keys := d.Push([]byte("\x1b[200~hello "))
	if len(keys) != 0 {
		t.Fatalf("first push: keys = %v, want none", keys)
	}
	if !d.pasting {
		t.Fatal("decoder should still be mid-paste after an unterminated start marker")
	}
	if string(d.pasteBuf) != "hello " {
		t.Fatalf("pasteBuf = %q, want %q", d.pasteBuf, "hello ")
	}

	keys = d.Push([]byte("world\x1b[201~"))
	if len(keys) != 1 {
		t.Fatalf("second push: keys = %v, want exactly 1", keys)
	}
	if keys[0].Kind != KeyPaste {
		t.Fatalf("keys[0].Kind = %v, want KeyPaste", keys[0].Kind)
	}
	if keys[0].Text != "hello world" {
		t.Fatalf("keys[0].Text = %q, want %q", keys[0].Text, "hello world")
	}
	if d.pasting {
		t.Fatal("decoder should have left paste mode")
	}
}

// TestDecoderPasteSplitAcrossThreePushCalls: the same gap, but the paste body
// arrives in three separate chunks before the end marker.
func TestDecoderPasteSplitAcrossThreePushCalls(t *testing.T) {
	var d Decoder
	if keys := d.Push([]byte("\x1b[200~foo")); len(keys) != 0 {
		t.Fatalf("chunk1: keys = %v", keys)
	}
	if keys := d.Push([]byte("bar")); len(keys) != 0 {
		t.Fatalf("chunk2: keys = %v", keys)
	}
	keys := d.Push([]byte("baz\x1b[201~"))
	if len(keys) != 1 || keys[0].Kind != KeyPaste || keys[0].Text != "foobarbaz" {
		t.Fatalf("keys = %v, want one KeyPaste %q", keys, "foobarbaz")
	}
}
