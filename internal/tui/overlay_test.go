package tui

import (
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/agent"
)

func TestApprovalOverlayRenderFits(t *testing.T) {
	o := newApprovalOverlay("rm -rf ./build", "dangerous", "", agent.ApprovalOriginMain)
	lines := o.renderInputPanel(8, 80)
	if len(lines) != 8 {
		t.Fatalf("expected 8 panel lines, got %d", len(lines))
	}
	for _, ln := range lines {
		if visibleWidth(ln) != 80 {
			t.Fatalf("line not full width: %d %q", visibleWidth(ln), stripANSI(ln))
		}
	}
}

func TestApprovalOverlayShowsRiskLine(t *testing.T) {
	o := newApprovalOverlay("rm -rf x", "cleanup", "/tmp", agent.ApprovalOriginMain)
	o.setRisk("high")
	lines := o.renderInputPanel(8, 80)
	found := false
	for _, ln := range lines {
		if strings.Contains(stripANSI(ln), "Risk: HIGH") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected risk line in %v", lines)
	}
}

func TestApprovalOverlayShowsPurposeLine(t *testing.T) {
	o := newApprovalOverlay("rm -rf ./build", "clean build artifacts", "", agent.ApprovalOriginMain)
	lines := o.renderInputPanel(8, 80)
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

func TestApprovalOverlayPurposePlaceholderWhenNoDescription(t *testing.T) {
	o := newApprovalOverlay("rm -rf x", "", "", agent.ApprovalOriginMain)
	lines := o.renderInputPanel(8, 60)
	found := false
	for _, ln := range lines {
		if strings.Contains(ln, "Purpose:") && strings.Contains(ln, "(no description provided)") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected placeholder purpose line, got %v", lines)
	}
}
