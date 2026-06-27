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
		{"\x03", false, true},
		{"x", false, false},
	}
	for _, tc := range cases {
		got, ok := approvalKeyAllowed([]byte(tc.key))
		if ok != tc.ok || (tc.ok && got != tc.want) {
			t.Errorf("key %q: allowed=%v ok=%v, want allowed=%v ok=%v", tc.key, got, ok, tc.want, tc.ok)
		}
	}
}

func TestApprovalKeyAllowedKittyEnter(t *testing.T) {
	// Kitty plain Enter → ESC[13u → decodeKittyKeys → \r → allow
	data := decodeKittyKeys([]byte{27, '[', '1', '3', 'u'})
	allowed, ok := approvalKeyAllowed(data)
	if !ok || !allowed {
		t.Fatalf("kitty Enter: allowed=%v ok=%v, want true true; data=%q", allowed, ok, data)
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

func TestApprovalOverlayShowsPurposeLine(t *testing.T) {
	o := newApprovalOverlay("rm -rf ./build", "clean build artifacts")
	_, lines := o.render(20, 80)
	foundCmd := false
	foundPurpose := false
	for _, ln := range lines {
		if strings.Contains(ln, "$ rm -rf ./build") {
			foundCmd = true
		}
		if strings.Contains(ln, "Purpose:") && strings.Contains(ln, "clean build artifacts") {
			foundPurpose = true
		}
	}
	if !foundCmd {
		t.Errorf("expected command line with $ prefix in %v", lines)
	}
	if !foundPurpose {
		t.Errorf("expected Purpose: line in %v", lines)
	}
}

func TestApprovalOverlayPurposeFallback(t *testing.T) {
	o := newApprovalOverlay("rm -rf x", "")
	_, lines := o.render(20, 60)
	foundFallback := false
	for _, ln := range lines {
		if strings.Contains(ln, "Purpose:") && strings.Contains(ln, "(no description provided)") {
			foundFallback = true
		}
	}
	if !foundFallback {
		t.Errorf("expected fallback Purpose in %v", lines)
	}
	// also test fallbackLines path
	fb := o.fallbackLines(40)
	foundFb := false
	for _, ln := range fb {
		if strings.Contains(ln, "Purpose:") && strings.Contains(ln, "(no description provided)") {
			foundFb = true
		}
	}
	if !foundFb {
		t.Errorf("expected fallback in fallbackLines %v", fb)
	}
}