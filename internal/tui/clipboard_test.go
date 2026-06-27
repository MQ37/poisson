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

func TestYankTextLastAssistant(t *testing.T) {
	s := newScrollback(1024)
	s.append(StyledLine{Style: styleUser, Text: "question"})
	s.append(StyledLine{Style: styleAssistant, Text: "answer one"})
	s.append(StyledLine{Style: styleAssistant, Text: " answer two"})
	if got := s.yankText(); got != "answer one answer two" {
		t.Fatalf("got %q", got)
	}
}

func TestYankTextFocusedTool(t *testing.T) {
	s := newScrollback(1024)
	s.append(StyledLine{Style: styleAssistant, Text: "ignored when tool focused"})
	s.appendToolCall(1, "", "bash", toolInputJSON("bash", map[string]string{"command": "echo hi"}))
	s.completeToolCall("", `{"stdout":"tool output","stderr":"","exitCode":0}`, "", 5)
	s.focusedToolID = s.blocks[1].id
	if got := s.yankText(); got != "tool output" {
		t.Fatalf("got %q", got)
	}
}

func TestYankTextEmpty(t *testing.T) {
	s := newScrollback(1024)
	if s.yankText() != "" {
		t.Fatal("expected empty")
	}
}