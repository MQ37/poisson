package tui

import (
	"bytes"
	"strings"
	"testing"
)

func TestFormatOsc52(t *testing.T) {
	got := formatOsc52("hi")
	want := "\x1b]52;c;aGk=\x1b\\"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestFormatOsc52Empty(t *testing.T) {
	if formatOsc52("") != "" {
		t.Fatal("empty text should produce no sequence")
	}
}

func TestOsc52CopyToUnicode(t *testing.T) {
	var buf bytes.Buffer
	text := "café ☕"
	if err := osc52CopyTo(text, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(buf.String(), "\x1b]52;c;") || !strings.HasSuffix(buf.String(), "\x1b\\") {
		t.Fatalf("bad framing: %q", buf.String())
	}
}

