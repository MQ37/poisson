package tui

import "testing"

func TestDecoderMetaBackspace(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
	}{
		{"esc del", []byte{27, 127}},
		{"esc bs", []byte{27, 8}},
		{"kitty alt bs pua", []byte{27, '[', '5', '7', '3', '4', '7', ';', '3', 'u'}},
		{"kitty alt bs ascii", []byte{27, '[', '1', '2', '7', ';', '3', 'u'}},
		{"xterm alt bs", []byte{27, '[', '1', '2', '7', ';', '3', ';', '1', '2', '7', '~'}},
	}
	for _, c := range cases {
		var d Decoder
		keys := d.Push(c.in)
		if len(keys) != 1 {
			t.Fatalf("%s: keys=%v want 1", c.name, keys)
		}
		if keys[0].Kind != KeyBackspace || !keys[0].Meta {
			t.Fatalf("%s: got %+v want Meta Backspace", c.name, keys[0])
		}
	}
}

func TestDecoderMetaArrows(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want KeyKind
	}{
		{"csi alt left", []byte{27, '[', '1', ';', '3', 'D'}, KeyArrowLeft},
		{"csi alt right", []byte{27, '[', '1', ';', '3', 'C'}, KeyArrowRight},
		{"kitty alt left", []byte{27, '[', '5', '7', '3', '5', '0', ';', '3', 'u'}, KeyArrowLeft},
		{"kitty alt right", []byte{27, '[', '5', '7', '3', '5', '1', ';', '3', 'u'}, KeyArrowRight},
		{"esc b", []byte{27, 'b'}, KeyArrowLeft},
		{"esc f", []byte{27, 'f'}, KeyArrowRight},
	}
	for _, c := range cases {
		var d Decoder
		keys := d.Push(c.in)
		if len(keys) != 1 {
			t.Fatalf("%s: keys=%v want 1", c.name, keys)
		}
		if keys[0].Kind != c.want || !keys[0].Meta {
			t.Fatalf("%s: got %+v want Meta %v", c.name, keys[0], c.want)
		}
	}
}

func TestEditorMetaBackspaceDeletesWord(t *testing.T) {
	e := newEditor()
	e.wrapWidth = 40
	e.insertText("hello world")
	e.col = e.runeCount(e.row)
	_, _ = e.applyKey(Key{Kind: KeyBackspace, Meta: true})
	if e.text() != "hello " {
		t.Fatalf("meta backspace = %q", e.text())
	}
}

func TestEditorMetaArrowsMoveByWord(t *testing.T) {
	e := newEditor()
	e.wrapWidth = 40
	e.insertText("one two three")
	e.col = e.runeCount(e.row)

	_, _ = e.applyKey(Key{Kind: KeyArrowLeft, Meta: true})
	if e.col != 8 {
		t.Fatalf("meta left col=%d want 8", e.col)
	}
	_, _ = e.applyKey(Key{Kind: KeyArrowLeft, Meta: true})
	if e.col != 4 {
		t.Fatalf("meta left col=%d want 4", e.col)
	}
	_, _ = e.applyKey(Key{Kind: KeyArrowRight, Meta: true})
	if e.col != 8 {
		t.Fatalf("meta right col=%d want 8", e.col)
	}
}

func TestFeedKeyMetaBackspaceInConvFocus(t *testing.T) {
	tui := newTestTUIHelper()
	tui.rows = 24
	tui.cols = 80
	tui.scrollRows = 16
	tui.scroll.append(StyledLine{Style: styleUser, Text: "hello"})
	tui.enterConvFocus()
	tui.editor.setText("foo bar baz")
	tui.editor.col = tui.editor.runeCount(tui.editor.row)

	_, err := tui.feed([]byte{27, 127})
	if err != nil {
		t.Fatal(err)
	}
	if tui.editor.text() != "foo bar " {
		t.Fatalf("editor = %q", tui.editor.text())
	}
}
