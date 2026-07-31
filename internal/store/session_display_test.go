package store

import "testing"

func TestDisplaySessionID(t *testing.T) {
	cases := []struct {
		id   string
		want string
	}{
		{"", ""},
		{"s-ab", "s-ab"},
		{"s-a3f9c1d2", "s-a3f9c1d2"},         // NewSessionID format (10 chars): fits, shown whole
		{"sub-a3f9c1d2", "sub-a3f9c1d2"},     // NewSubagentID format (12 chars): fits exactly, shown whole
		{"sub-a3f9c1d2x", "sub-a3f9c1d2…"},   // one char over: truncated with an ellipsis
		{"some-unusually-long-legacy-id", "some-unusual…"},
	}
	for _, c := range cases {
		if got := DisplaySessionID(c.id); got != c.want {
			t.Errorf("DisplaySessionID(%q) = %q, want %q", c.id, got, c.want)
		}
	}
}

// TestDisplaySessionIDMatchesRealIDFormats guards the constant itself: if
// NewSessionID/NewSubagentID ever grow, this fails loudly instead of
// silently starting to truncate ids that used to always display in full.
func TestDisplaySessionIDMatchesRealIDFormats(t *testing.T) {
	if id := NewSessionID(); len(id) > DisplaySessionIDMaxLen {
		t.Errorf("NewSessionID() = %q (%d chars) exceeds DisplaySessionIDMaxLen (%d) — it would now display truncated", id, len(id), DisplaySessionIDMaxLen)
	}
	if id := NewSubagentID(); len(id) > DisplaySessionIDMaxLen {
		t.Errorf("NewSubagentID() = %q (%d chars) exceeds DisplaySessionIDMaxLen (%d) — it would now display truncated", id, len(id), DisplaySessionIDMaxLen)
	}
}
