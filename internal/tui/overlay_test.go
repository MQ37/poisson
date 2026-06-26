package tui

import "testing"

func TestApprovalKeyAllowed(t *testing.T) {
	cases := []struct {
		key  string
		want bool
		ok   bool
	}{
		{"a", true, true},
		{"A", true, true},
		{"y", true, true},
		{"d", false, true},
		{"n", false, true},
		{"\x1b", false, true},
		{"x", false, false},
	}
	for _, tc := range cases {
		got, ok := approvalKeyAllowed([]byte(tc.key))
		if ok != tc.ok || (tc.ok && got != tc.want) {
			t.Errorf("key %q: allowed=%v ok=%v, want allowed=%v ok=%v", tc.key, got, ok, tc.want, tc.ok)
		}
	}
}

func TestApprovalOverlayRenderFits(t *testing.T) {
	o := newApprovalOverlay("rm -rf ./build", "dangerous")
	anchor, lines := o.render(20, 80)
	if anchor < 1 || anchor > 20 {
		t.Fatalf("anchor = %d", anchor)
	}
	if len(lines) < 4 {
		t.Fatalf("expected box lines, got %d", len(lines))
	}
	for _, ln := range lines {
		if visibleWidth(ln) > 80 {
			t.Fatalf("line too wide: %d %q", visibleWidth(ln), ln)
		}
	}
}