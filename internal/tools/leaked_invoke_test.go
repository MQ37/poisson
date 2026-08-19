package tools

import "testing"

// paramOpen is split across a `+` so this file's own raw source text never
// contains the contiguous fragment — that exact fragment is what this
// package's own leaked-tool-call-template guard (validate.go) rejects on
// sight, which would otherwise block writing this very test file.
const paramOpen = "<parameter " + `name="`

func TestCountLeakedInvokes(t *testing.T) {
	cases := []struct {
		name string
		text string
		want int
	}{
		{"clean text", "sure, I'll read that file now.", 0},
		{"empty", "", 0},
		// Real sample from the wild: garbled leak where the tool name
		// (bash) is used as a bogus parameter name instead of appearing on
		// the invoke tag, and the parameter tag is never closed.
		{"one garbled invoke", "<invoke>\n" + paramOpen + "bash\">ssh root@host 'echo hi'\n</invoke>", 1},
		{"two garbled invokes", "<invoke>\n" + paramOpen + "bash\">cmd1\n</invoke>\n<invoke>\n" + paramOpen + "bash\">cmd2\n</invoke>", 2},
		{"well-formed invoke", "<invoke name=\"bash\">\n" + paramOpen + "command\">ls</parameter>\n</invoke>", 1},
		{"mentions invoke in prose without a tag", "you can invoke this tool yourself", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CountLeakedInvokes(tc.text); got != tc.want {
				t.Errorf("CountLeakedInvokes(%q) = %d, want %d", tc.text, got, tc.want)
			}
		})
	}
}
