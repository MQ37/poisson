package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/tools"
)

// runBashTurn drives one bash tool_use through Prompt with the given
// approvalFn and returns the OutputToolResult event for the bash call, so
// each case below only needs to assert on ev.HumanApproval.
func runBashTurn(t *testing.T, approvalFn func(ctx context.Context, command, description, workdir string) (bool, string)) OutputEvent {
	t.Helper()
	s := newTestStore(t)
	sid := newTestSession(t, s, "m")

	prov := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	first, second := provider.FakeToolCallResponse("bash", map[string]string{"command": "ls", "description": "look around"}, "done")
	prov.SetResponses([][]provider.StreamEvent{first, second})

	reg := tools.NewRegistry()
	reg.Register(tools.NewBashTool(".", approvalFn))

	ch := make(chan OutputEvent, 32)
	events, drained := drainEvents(ch)
	a := NewAgent(s, prov, reg, newTestConfig(), sid, ch, approvalFn)
	a.SetModel("m")

	if err := a.Prompt("look around"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	close(ch)
	<-drained // wait for the drain goroutine's last append before reading *events

	for _, ev := range *events {
		if ev.Type == OutputToolResult && ev.ToolName == "bash" {
			return ev
		}
	}
	t.Fatal("no bash tool_result event")
	return OutputEvent{}
}

// TestDispatchMarksHumanApproved is the end-to-end proof of the conversation
// view's approved marker: an ApprovalFn that behaves like the interactive
// TUI's Approve (reports its decision via tools.RecordApproval, exactly what
// tui.TUI.Approve does) produces a tool_result event with HumanApproval ==
// "approved" — with zero per-tool wiring beyond the existing ApprovalFn hook.
func TestDispatchMarksHumanApproved(t *testing.T) {
	approvalFn := func(ctx context.Context, command, description, workdir string) (bool, string) {
		tools.RecordApproval(ctx, true)
		return true, ""
	}
	ev := runBashTurn(t, approvalFn)
	if ev.HumanApproval != "approved" {
		t.Fatalf("HumanApproval = %q, want approved", ev.HumanApproval)
	}
	if ev.ToolError != "" {
		t.Fatalf("unexpected ToolError: %q", ev.ToolError)
	}
}

// TestDispatchMarksHumanDenied is the denied counterpart: bash.go's own
// rejection message still lands in ToolError, but the marker now
// distinguishes "a human stopped this" from "it ran and failed" without the
// human having to read the error text.
func TestDispatchMarksHumanDenied(t *testing.T) {
	approvalFn := func(ctx context.Context, command, description, workdir string) (bool, string) {
		tools.RecordApproval(ctx, false)
		return false, "not today"
	}
	ev := runBashTurn(t, approvalFn)
	if ev.HumanApproval != "denied" {
		t.Fatalf("HumanApproval = %q, want denied", ev.HumanApproval)
	}
	if ev.ToolError == "" {
		t.Fatal("expected ToolError for a denied command")
	}
}

// TestDispatchNoMarkerWhenNeverAsked is the common, silent case: an
// ApprovalFn that auto-approves without ever calling RecordApproval (exactly
// what the guard fast path and the LLM low-risk classifier do — see
// WrapRiskGatedApproval) must leave HumanApproval empty, so no marker
// appears for the vast majority of commands that never reach a human.
func TestDispatchNoMarkerWhenNeverAsked(t *testing.T) {
	approvalFn := func(ctx context.Context, command, description, workdir string) (bool, string) {
		return true, "" // auto-approved, no RecordApproval call
	}
	ev := runBashTurn(t, approvalFn)
	if ev.HumanApproval != "" {
		t.Fatalf("HumanApproval = %q, want empty (never asked)", ev.HumanApproval)
	}
}

// runBashTurnCapturingRequests is runBashTurn plus access to the fake
// provider, so a test can inspect the wire tool_result content sent on the
// turn's next Stream call — where humanApprovalNudgeFor actually lands (see
// agent.go's persistence loop), as opposed to ev.HumanApproval (the UI
// marker, asserted by the tests above).
func runBashTurnCapturingRequests(t *testing.T, approvalFn func(ctx context.Context, command, description, workdir string) (bool, string)) *provider.FakeProvider {
	t.Helper()
	s := newTestStore(t)
	sid := newTestSession(t, s, "m")

	prov := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	first, second := provider.FakeToolCallResponse("bash", map[string]string{"command": "ls", "description": "look around"}, "done")
	prov.SetResponses([][]provider.StreamEvent{first, second})

	reg := tools.NewRegistry()
	reg.Register(tools.NewBashTool(".", approvalFn))

	ch := make(chan OutputEvent, 32)
	_, drained := drainEvents(ch)
	a := NewAgent(s, prov, reg, newTestConfig(), sid, ch, approvalFn)
	a.SetModel("m")

	if err := a.Prompt("look around"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	close(ch)
	<-drained
	return prov
}

// toolResultTextFromRequest returns the tool-role message's ToolResult text
// from req (the request built for the turn's round AFTER the tool ran),
// where humanApprovalNudgeFor's output actually lands.
func toolResultTextFromRequest(t *testing.T, req *provider.Request) string {
	t.Helper()
	for _, m := range req.Messages {
		if m.Role != "tool" {
			continue
		}
		for _, b := range m.Content {
			if b.Type == "tool_result" {
				return b.ToolResult
			}
		}
	}
	t.Fatal("no tool_result content block found in request")
	return ""
}

// TestDispatchAppendsNudgeWhenAsked is the wire-content counterpart of
// TestDispatchMarksHumanApproved: the model's own tool_result — what it
// actually reads on the next turn — must carry humanApprovalNudge whenever
// a human was really asked, regardless of the decision.
func TestDispatchAppendsNudgeWhenAsked(t *testing.T) {
	approvalFn := func(ctx context.Context, command, description, workdir string) (bool, string) {
		tools.RecordApproval(ctx, true)
		return true, ""
	}
	prov := runBashTurnCapturingRequests(t, approvalFn)
	got := toolResultTextFromRequest(t, prov.LastRequest())
	if !strings.Contains(got, humanApprovalNudge) {
		t.Fatalf("tool_result = %q, want it to contain the nudge", got)
	}
}

// TestDispatchNoNudgeWhenNeverAsked is the wire-content counterpart of
// TestDispatchNoMarkerWhenNeverAsked: auto-approved calls must not carry the
// nudge — it would be noise on the vast majority of tool calls.
func TestDispatchNoNudgeWhenNeverAsked(t *testing.T) {
	approvalFn := func(ctx context.Context, command, description, workdir string) (bool, string) {
		return true, "" // auto-approved, no RecordApproval call
	}
	prov := runBashTurnCapturingRequests(t, approvalFn)
	got := toolResultTextFromRequest(t, prov.LastRequest())
	if strings.Contains(got, humanApprovalNudge) {
		t.Fatalf("tool_result = %q, want no nudge (never asked)", got)
	}
}
