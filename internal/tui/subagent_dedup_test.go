package tui

import "testing"

func TestDedupName(t *testing.T) {
	cases := map[string]string{
		"compute-2plus2 compute-2plus2": "compute-2plus2",
		"calc-subagent calc-subagent":   "calc-subagent",
		"code reviewer":                 "code reviewer",
		"explore":                       "explore",
		"":                              "",
		"a a a":                         "a",
	}
	for in, want := range cases {
		if got := dedupAdjacentWords(in); got != want {
			t.Errorf("dedupAdjacentWords(%q) = %q, want %q", in, got, want)
		}
	}
}
