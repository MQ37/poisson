package tui

import (
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/agent"
)

func TestSubagentDoneNotificationText(t *testing.T) {
	ok := subagentDoneNotificationText("sub-abc", "", false)
	if !strings.Contains(ok, "sub-abc") || !strings.Contains(ok, "finished") {
		t.Fatalf("success text = %q, want it to mention the job id and finished", ok)
	}
	failed := subagentDoneNotificationText("sub-abc", "boom", false)
	if !strings.Contains(failed, "sub-abc") || !strings.Contains(failed, "failed") {
		t.Fatalf("error text = %q, want it to mention the job id and failed", failed)
	}
}

// TestSubagentDoneNotificationText_Killed is the regression test for the
// bug where an explicit subagent_kill was narrated to the main agent as a
// failure — killed jobs carry the same generic "subagent cancelled"
// errMsg an ordinary timeout/shutdown cancellation does, but killed must
// win and produce neutral wording, not "failed".
func TestSubagentDoneNotificationText_Killed(t *testing.T) {
	killed := subagentDoneNotificationText("sub-abc", "subagent cancelled", true)
	if !strings.Contains(killed, "sub-abc") || !strings.Contains(killed, "killed") {
		t.Fatalf("killed text = %q, want it to mention the job id and killed", killed)
	}
	if strings.Contains(killed, "failed") {
		t.Fatalf("killed text = %q, must not say failed", killed)
	}
}

// TestInjectSubagentDoneNotification_IdleStartsTurn: main agent idle, no
// overlay — the nudge is displayed and a fresh turn starts immediately
// (checked before releasing t.mu, so there's no race against how fast the
// spawned turn goroutine itself runs).
func TestInjectSubagentDoneNotification_IdleStartsTurn(t *testing.T) {
	e := newTUIIntegEnv(t, nil)
	e.tui.mu.Lock()
	e.tui.injectSubagentDoneNotification("sub-1", "", false)
	thinking := e.tui.status.Thinking
	out := testScrollOutput(e.tui)
	e.tui.mu.Unlock()

	if !thinking {
		t.Fatal("expected a turn to start immediately while the main agent was idle")
	}
	if !strings.Contains(out, "sub-1") || !strings.Contains(out, "finished") {
		t.Fatalf("scrollback = %q, want the nudge text appended", out)
	}
}

// TestInjectSubagentDoneNotification_BusyQueues: a turn is already running —
// the nudge must queue exactly like a user message typed while busy, not
// start a second concurrent turn.
func TestInjectSubagentDoneNotification_BusyQueues(t *testing.T) {
	e := newTUIIntegEnv(t, nil)
	e.tui.mu.Lock()
	e.tui.status.Thinking = true
	e.tui.injectSubagentDoneNotification("sub-1", "", false)
	n := len(e.tui.queued)
	var got string
	if n > 0 {
		got = e.tui.queued[0]
	}
	e.tui.mu.Unlock()

	if n != 1 {
		t.Fatalf("queued = %d, want exactly 1", n)
	}
	if !strings.Contains(got, "sub-1") {
		t.Fatalf("queued message = %q, want the nudge text", got)
	}
}

// TestInjectSubagentDoneNotification_CompactingQueues: sessionBusyLocked
// also covers a manual /compact running with no turn in flight.
func TestInjectSubagentDoneNotification_CompactingQueues(t *testing.T) {
	e := newTUIIntegEnv(t, nil)
	e.tui.mu.Lock()
	e.tui.compacting.Store(true)
	e.tui.injectSubagentDoneNotification("sub-1", "", false)
	n := len(e.tui.queued)
	e.tui.mu.Unlock()

	if n != 1 {
		t.Fatalf("queued = %d, want exactly 1 while compacting", n)
	}
}

// TestHandleEvent_SubagentJobFinishedRoutesToInject proves handleEvent's
// dispatch for the new event type actually reaches
// injectSubagentDoneNotification, not just that the helper works in
// isolation.
func TestHandleEvent_SubagentJobFinishedRoutesToInject(t *testing.T) {
	e := newTUIIntegEnv(t, nil)
	e.tui.mu.Lock()
	e.tui.handleEvent(agent.OutputEvent{
		Type:       agent.OutputSubagentJobFinished,
		ToolCallID: "sub-1",
	})
	thinking := e.tui.status.Thinking
	out := testScrollOutput(e.tui)
	e.tui.mu.Unlock()

	if !thinking {
		t.Fatal("expected handleEvent to start a turn via injectSubagentDoneNotification")
	}
	if !strings.Contains(out, "sub-1") {
		t.Fatalf("scrollback = %q, want the nudge text", out)
	}
}

// TestHandleEvent_SubagentKilledDoesNotSayFailed is the full-path
// regression test: a killed job's event carries the same non-empty
// ToolError a real failure would, but handleEvent must route it to the
// neutral "killed" wording, never "failed".
func TestHandleEvent_SubagentKilledDoesNotSayFailed(t *testing.T) {
	e := newTUIIntegEnv(t, nil)
	e.tui.mu.Lock()
	e.tui.handleEvent(agent.OutputEvent{
		Type:           agent.OutputSubagentJobFinished,
		ToolCallID:     "sub-1",
		ToolError:      "subagent cancelled",
		SubagentKilled: true,
	})
	out := testScrollOutput(e.tui)
	e.tui.mu.Unlock()

	if !strings.Contains(out, "killed") {
		t.Fatalf("scrollback = %q, want the killed nudge text", out)
	}
	if strings.Contains(out, "failed") {
		t.Fatalf("scrollback = %q, must not say failed for an explicit kill", out)
	}
}

// TestInjectSubagentDoneNotification_OverlayActiveQueues: main agent idle
// (sessionBusyLocked false) but an unrelated modal overlay is up — e.g. a
// background subagent's own bash-approval prompt, or /btw. Starting a fresh
// turn on top of that would be confusing even though nothing is "running"
// in the sessionBusyLocked sense.
func TestInjectSubagentDoneNotification_OverlayActiveQueues(t *testing.T) {
	e := newTUIIntegEnv(t, nil)
	e.tui.mu.Lock()
	e.tui.activeOverlay = newApprovalOverlay("ls", "list", "", agent.ApprovalOriginMain)
	e.tui.injectSubagentDoneNotification("sub-1", "", false)
	n := len(e.tui.queued)
	thinking := e.tui.status.Thinking
	e.tui.mu.Unlock()

	if thinking {
		t.Fatal("a turn should not start while an unrelated overlay is active")
	}
	if n != 1 {
		t.Fatalf("queued = %d, want exactly 1 while an overlay is active", n)
	}
}
