package agent

import (
	"testing"

	"github.com/mq37/poisson/internal/tools"
)

// TestCompleteSubagentJob_SameSessionNotifies is the common case: a job
// finishes while its spawning session is still the live one.
func TestCompleteSubagentJob_SameSessionNotifies(t *testing.T) {
	ch := make(chan OutputEvent, 8)
	a := newTestAgentForSpeed(t, ch)

	a.CompleteSubagentJob("sub-1", a.SessionID(), "call-1", tools.ToolResult{Content: "did the thing"}, false)

	// Widget-completion event always fires first, keyed by the spawning
	// tool-call id (not the job id) — see OutputSubagentJobResult's doc
	// comment for why.
	ev, ok := drainOne(ch)
	if !ok || ev.Type != OutputSubagentJobResult || ev.ToolName != "subagent" || ev.ToolCallID != "call-1" {
		t.Fatalf("expected the widget-completion OutputSubagentJobResult first, got %+v (ok=%v)", ev, ok)
	}
	ev, ok = drainOne(ch)
	if !ok || ev.Type != OutputSubagentJobFinished || ev.ToolCallID != "sub-1" {
		t.Fatalf("expected OutputSubagentJobFinished, got %+v (ok=%v)", ev, ok)
	}
	if ev.ToolResultContent != "did the thing" {
		t.Fatalf("ToolResultContent = %q, want the job's result content", ev.ToolResultContent)
	}
}

// TestCompleteSubagentJob_DifferentSessionSkipsNotify is the regression test
// for the cross-session-leakage finding in docs/async-subagent-plan.md phase
// 3: a job spawned under a session the user has since switched away from
// must not inject anything (or start a turn) into whatever session is live
// now — but the widget-completion event still fires, since a stale widget
// from the old session simply won't be found (scrollback was replaced).
func TestCompleteSubagentJob_DifferentSessionSkipsNotify(t *testing.T) {
	ch := make(chan OutputEvent, 8)
	a := newTestAgentForSpeed(t, ch)

	a.CompleteSubagentJob("sub-1", "some-other-session-entirely", "call-1", tools.ToolResult{Content: "did the thing"}, false)

	ev, ok := drainOne(ch)
	if !ok || ev.Type != OutputSubagentJobResult || ev.ToolName != "subagent" {
		t.Fatalf("expected the widget-completion OutputSubagentJobResult, got %+v (ok=%v)", ev, ok)
	}
	if ev, ok := drainOne(ch); ok {
		t.Fatalf("expected no OutputSubagentJobFinished for a different session, got %+v", ev)
	}
}

// TestCompleteSubagentJob_EmptySessionIDAlwaysNotifies covers the fail-open
// default: sessionID == "" means session tracking wasn't wired at spawn
// time (e.g. SubagentTool.SetSessionIDFn never called) — must never block
// the notify, matching SubagentTool's own fail-open framing.
func TestCompleteSubagentJob_EmptySessionIDAlwaysNotifies(t *testing.T) {
	ch := make(chan OutputEvent, 8)
	a := newTestAgentForSpeed(t, ch)

	a.CompleteSubagentJob("sub-1", "", "call-1", tools.ToolResult{Content: "did the thing"}, false)

	drainOne(ch) // widget completion
	ev, ok := drainOne(ch)
	if !ok || ev.Type != OutputSubagentJobFinished {
		t.Fatalf("expected OutputSubagentJobFinished with an empty sessionID, got %+v (ok=%v)", ev, ok)
	}
}
