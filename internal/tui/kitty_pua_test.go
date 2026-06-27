package tui

import "testing"

func TestKittyPUAFunctionalKeys(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want []byte
	}{
		{"escape", []byte{27, '[', '5', '7', '3', '4', '4', 'u'}, []byte{27}},
		{"enter", []byte{27, '[', '5', '7', '3', '4', '5', 'u'}, []byte{'\r'}},
		{"tab", []byte{27, '[', '5', '7', '3', '4', '6', 'u'}, []byte{9}},
		{"backspace", []byte{27, '[', '5', '7', '3', '4', '7', 'u'}, []byte{127}},
		{"delete", []byte{27, '[', '5', '7', '3', '4', '8', 'u'}, []byte("\x1b[3~")},
		{"insert", []byte{27, '[', '5', '7', '3', '4', '9', 'u'}, []byte("\x1b[2~")},
	}
	for _, c := range cases {
		got := decodeKittyKeys(c.in)
		if string(got) != string(c.want) {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}