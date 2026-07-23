package tui

import (
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/agent"
)

// TestToggleApprovalMode_ShiftTabFlipsAgentAndStatus verifies Shift+Tab
// flips the agent's ApprovalMode both ways and mirrors it into the status
// snapshot the render path reads.
func TestToggleApprovalMode_ShiftTabFlipsAgentAndStatus(t *testing.T) {
	e := newTUIIntegEnv(t, nil)
	tui := e.tui

	if tui.agent.ApprovalMode() != agent.ApprovalModeFast {
		t.Fatal("expected default ApprovalMode to be Fast")
	}

	if _, err := tui.feedKey(Key{Kind: KeyShiftTab}); err != nil {
		t.Fatalf("feedKey: %v", err)
	}
	if tui.agent.ApprovalMode() != agent.ApprovalModeParanoid {
		t.Fatal("expected Shift+Tab to switch to Paranoid")
	}
	if tui.status.ApprovalMode != agent.ApprovalModeParanoid {
		t.Fatal("expected status.ApprovalMode to mirror the agent")
	}
	if !strings.Contains(tui.status.Hint, "paranoid") {
		t.Errorf("hint = %q, want a paranoid-mode notice", tui.status.Hint)
	}

	if _, err := tui.feedKey(Key{Kind: KeyShiftTab}); err != nil {
		t.Fatalf("feedKey: %v", err)
	}
	if tui.agent.ApprovalMode() != agent.ApprovalModeFast {
		t.Fatal("expected second Shift+Tab to switch back to Fast")
	}
	if tui.status.ApprovalMode != agent.ApprovalModeFast {
		t.Fatal("expected status.ApprovalMode to mirror the agent back to Fast")
	}
}

// TestRenderHintLine_ShowsModeBottomRight verifies the mode tag is
// right-aligned at the end of the hint line and the left keybinding hint
// text is still present.
func TestRenderHintLine_ShowsModeBottomRight(t *testing.T) {
	tui := newTestTUIHelper()
	const width = 220 // wide enough to fit the full keybinding hint AND the mode tag
	tui.status.ApprovalMode = agent.ApprovalModeFast
	line := stripANSI(tui.renderHintLine(width))
	if !strings.Contains(line, "Tab:conv") {
		t.Errorf("line = %q, want the usual keybinding hint preserved", line)
	}
	if !strings.HasSuffix(strings.TrimRight(line, " "), "FAST") {
		t.Errorf("line = %q, want it to end with the FAST tag", line)
	}

	tui.status.ApprovalMode = agent.ApprovalModeParanoid
	line = stripANSI(tui.renderHintLine(width))
	if !strings.HasSuffix(strings.TrimRight(line, " "), "PARANOID") {
		t.Errorf("line = %q, want it to end with the PARANOID tag", line)
	}
}

// TestRenderHintLine_DropsTagWhenNoRoom verifies a too-narrow width keeps the
// hint text (never sacrificed) and simply omits the mode tag rather than
// corrupting the line.
func TestRenderHintLine_DropsTagWhenNoRoom(t *testing.T) {
	tui := newTestTUIHelper()
	tui.status.ApprovalMode = agent.ApprovalModeParanoid
	line := stripANSI(tui.renderHintLine(10))
	if strings.Contains(line, "PARANOID") {
		t.Errorf("line = %q, want mode tag dropped when there's no room", line)
	}
	if !strings.Contains(line, "Tab") {
		t.Errorf("line = %q, want the hint text kept even when truncated", line)
	}
}
