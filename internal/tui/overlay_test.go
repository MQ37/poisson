package tui

import (
	"strings"
	"testing"
)

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
		{"\x03", false, false},
		{"x", false, false},
		{"\r", true, true},
	}
	for _, tc := range cases {
		got, ok := approvalKeyAllowed([]byte(tc.key))
		if ok != tc.ok || (tc.ok && got != tc.want) {
			t.Errorf("key %q: allowed=%v ok=%v, want allowed=%v ok=%v", tc.key, got, ok, tc.want, tc.ok)
		}
	}
}

func TestApprovalKeyAllowedIgnoresArrowCSI(t *testing.T) {
	allowed, ok := approvalKeyAllowed(arrowDownBytes())
	if ok {
		t.Fatalf("arrow during approval should be ignored, got allowed=%v ok=%v", allowed, ok)
	}
}

func TestApprovalOverlayRenderFits(t *testing.T) {
	o := newApprovalOverlay("rm -rf ./build", "dangerous", "")
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

func TestApprovalOverlayShowsPurposeLine(t *testing.T) {
	o := newApprovalOverlay("rm -rf ./build", "clean build artifacts", "")
	_, lines := o.render(20, 80)
	foundCmd := false
	foundPurpose := false
	for _, ln := range lines {
		plain := stripANSI(ln)
		if strings.Contains(plain, "█") && strings.Contains(plain, "$") && strings.Contains(plain, "rm -rf ./build") {
			foundCmd = true
		}
		if strings.Contains(plain, "Purpose:") && strings.Contains(plain, "clean build artifacts") {
			foundPurpose = true
		}
	}
	if !foundCmd {
		t.Errorf("expected highlighted command with approval bar in %v", lines)
	}
	if !foundPurpose {
		t.Errorf("expected Purpose: line in %v", lines)
	}
}

func TestApprovalOverlayPurposeFallbackGuardReason(t *testing.T) {
	o := newApprovalOverlay("rm -rf x", "", "")
	_, lines := o.render(20, 60)
	found := false
	for _, ln := range lines {
		if strings.Contains(ln, "Purpose:") && strings.Contains(ln, "destructive command") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected guard reason in Purpose line, got %v", lines)
	}
}