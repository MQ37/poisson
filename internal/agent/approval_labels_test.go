package agent

import "testing"

// TestApprovalModeString covers ApprovalMode's label for every constant —
// no test existed for this before.
func TestApprovalModeString(t *testing.T) {
	cases := []struct {
		mode ApprovalMode
		want string
	}{
		{ApprovalModeFast, "fast"},
		{ApprovalModeParanoid, "paranoid"},
		{ApprovalModeYolo, "yolo"},
	}
	for _, c := range cases {
		if got := c.mode.String(); got != c.want {
			t.Errorf("ApprovalMode(%d).String() = %q, want %q", c.mode, got, c.want)
		}
	}
}

// TestSubagentOrigin covers both the unnamed and named formats — no test
// existed for this before.
func TestSubagentOrigin(t *testing.T) {
	if got := SubagentOrigin(""); got != ApprovalOrigin("subagent") {
		t.Errorf("SubagentOrigin(\"\") = %q, want %q", got, "subagent")
	}
	if got := SubagentOrigin("scout"); got != ApprovalOrigin("subagent:scout") {
		t.Errorf("SubagentOrigin(\"scout\") = %q, want %q", got, "subagent:scout")
	}
}
