package orchestrator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/subagent"
)

// newCommandTestCore builds a Core with a fresh instance already created —
// the common setup for every per-instance command test below.
func newCommandTestCore(t *testing.T) (core *Core, rt *FakeRuntime, ff *FakeFrontend, instKey ChannelKey, cancel func()) {
	t.Helper()
	cfg := testCoreConfig(t)
	rt = NewFakeRuntime().WithStateRoot(cfg.StateRoot)
	ff = NewFakeFrontend()
	core = NewCore(rt, ff, cfg)
	cancel = startCore(t, core)

	ff.Inject(Command{Kind: CmdNew, Args: []string{"cmdtest"}, Key: generalKey})
	waitFor(t, time.Second, func() bool { return sentContains(ff, "created and running") })
	return core, rt, ff, ChannelKey{Frontend: "fake", Chat: "fake-chat", Topic: "1"}, cancel
}

func TestCommands_ModelAllowedUpdatesTakesEffectNextTurn(t *testing.T) {
	core, _, ff, instKey, cancel := newCommandTestCore(t)
	defer cancel()

	ff.Inject(Command{Kind: CmdModel, Args: []string{"xai/grok-build"}, Key: instKey})
	waitFor(t, time.Second, func() bool { return sentContains(ff, "takes effect on the NEXT turn") })

	if got := instanceModel(t, core, "px-cmdtest"); got != "xai/grok-build" {
		t.Errorf("model = %q, want xai/grok-build", got)
	}
}

func TestCommands_ModelRejectsUnlistedValue(t *testing.T) {
	core, _, ff, instKey, cancel := newCommandTestCore(t)
	defer cancel()

	ff.Inject(Command{Kind: CmdModel, Args: []string{"openai/gpt-5.6-sol"}, Key: instKey})
	waitFor(t, time.Second, func() bool { return sentContains(ff, "not on the allow-list") })

	if got := instanceModel(t, core, "px-cmdtest"); got != "anthropic/claude-sonnet-5" {
		t.Errorf("model changed to %q despite rejection, want unchanged anthropic/claude-sonnet-5", got)
	}
}

func TestCommands_ModelNoArgsShowsUsage(t *testing.T) {
	core, _, ff, instKey, cancel := newCommandTestCore(t)
	defer cancel()
	_ = core

	ff.Inject(Command{Kind: CmdModel, Args: nil, Key: instKey})
	waitFor(t, time.Second, func() bool { return sentContains(ff, "usage: /model") })
}

func TestCommands_ResumeOnNeverSuspendedInstanceIsSpecificError(t *testing.T) {
	_, _, ff, instKey, cancel := newCommandTestCore(t)
	defer cancel()

	ff.Inject(Command{Kind: CmdResume, Key: instKey})
	waitFor(t, time.Second, func() bool { return sentContains(ff, "isn't suspended") })
}

func TestCommands_SuspendThenResume(t *testing.T) {
	core, rt, ff, instKey, cancel := newCommandTestCore(t)
	defer cancel()

	ff.Inject(Command{Kind: CmdSuspend, Key: instKey})
	waitFor(t, time.Second, func() bool { return sentContains(ff, "suspended") })
	if ds := instanceDesiredState(t, core, "px-cmdtest"); ds != DesiredSuspended {
		t.Errorf("DesiredState = %v, want suspended", ds)
	}
	infos, _ := rt.List(context.Background())
	if len(infos) != 1 || infos[0].State != "stopped" {
		t.Fatalf("rt.List() = %+v, want stopped", infos)
	}

	ff.Inject(Command{Kind: CmdResume, Key: instKey})
	waitFor(t, time.Second, func() bool { return sentContains(ff, "resumed") })
	if ds := instanceDesiredState(t, core, "px-cmdtest"); ds != DesiredRunning {
		t.Errorf("DesiredState = %v, want running", ds)
	}
	infos, _ = rt.List(context.Background())
	if len(infos) != 1 || infos[0].State != "running" {
		t.Fatalf("rt.List() = %+v, want running", infos)
	}
}

func TestCommands_KillFullyRemovesInstance(t *testing.T) {
	core, rt, ff, instKey, cancel := newCommandTestCore(t)
	defer cancel()

	ff.Inject(Command{Kind: CmdKill, Key: instKey})
	waitFor(t, time.Second, func() bool {
		infos, _ := rt.List(context.Background())
		return len(infos) == 0
	})

	if closed := ff.ClosedChannels(); len(closed) != 1 || closed[0] != instKey {
		t.Errorf("ClosedChannels() = %+v, want exactly %+v", closed, instKey)
	}

	core.mu.Lock()
	_, stillRegistered := core.byKey[instKey]
	core.mu.Unlock()
	if stillRegistered {
		t.Error("instance still registered after /kill")
	}

	// A message to the now-gone instance gets the "no instance" reply, not
	// silence and not a panic.
	ff.Inject(Command{Kind: CmdMessage, Text: "hello?", Key: instKey})
	waitFor(t, time.Second, func() bool { return sentContains(ff, "no instance here") })
}

func TestCommands_KillMidTurnStopsTheTurnFirst(t *testing.T) {
	core, rt, ff, instKey, cancel := newCommandTestCore(t)
	defer cancel()

	rt.ScriptTurn("px-cmdtest", TurnScript{Hang: true})
	ff.Inject(Command{Kind: CmdMessage, Text: "go", Key: instKey})
	waitFor(t, time.Second, func() bool { return rt.StartTurnCallCount() >= 1 })

	ff.Inject(Command{Kind: CmdKill, Key: instKey})
	waitFor(t, time.Second, func() bool {
		infos, _ := rt.List(context.Background())
		return len(infos) == 0
	})
	_ = core
}

func TestCommands_ApproveWithNoPendingApprovalIsSpecificError(t *testing.T) {
	_, _, ff, instKey, cancel := newCommandTestCore(t)
	defer cancel()

	ff.Inject(Command{Kind: CmdApprove, Key: instKey})
	waitFor(t, time.Second, func() bool { return sentContains(ff, "no approval is currently pending") })
}

func TestCommands_ApproveDenyRoundTrip(t *testing.T) {
	core, rt, ff, instKey, cancel := newCommandTestCore(t)
	defer cancel()

	// A turn that emits one approval_request and then hangs (a real one
	// would block waiting for the approval_response — the fake models that
	// exactly the same way, by simply not scripting anything past it).
	rt.ScriptTurn("px-cmdtest", TurnScript{Hang: true, Events: []subagent.ChildEvent{
		{Type: "approval_request", Command: "rm -rf /tmp/x", Risk: "high"},
	}})
	ff.Inject(Command{Kind: CmdMessage, Text: "run something risky", Key: instKey})
	waitFor(t, time.Second, func() bool { return sentContains(ff, "approval needed") })

	if p := instancePending(t, core, "px-cmdtest"); p == nil {
		t.Fatal("expected a pending approval to be recorded")
	}

	ff.Inject(Command{Kind: CmdDeny, Args: []string{"looks", "risky"}, Key: instKey})
	waitFor(t, time.Second, func() bool { return rt.StdinWritten("px-cmdtest") != "" })
	if !strings.Contains(rt.StdinWritten("px-cmdtest"), `"approved":false`) {
		t.Errorf("stdin written = %q, want approved:false", rt.StdinWritten("px-cmdtest"))
	}
	if !strings.Contains(rt.StdinWritten("px-cmdtest"), "looks risky") {
		t.Errorf("stdin written = %q, want the deny reason", rt.StdinWritten("px-cmdtest"))
	}
}

func TestCommands_CancelWithNoTurnRunningIsSpecificError(t *testing.T) {
	_, _, ff, instKey, cancel := newCommandTestCore(t)
	defer cancel()

	ff.Inject(Command{Kind: CmdCancel, Key: instKey})
	waitFor(t, time.Second, func() bool { return sentContains(ff, "no turn is currently running") })
}

func TestCommands_HelpReplies(t *testing.T) {
	_, _, ff, instKey, cancel := newCommandTestCore(t)
	defer cancel()

	ff.Inject(Command{Kind: CmdHelp, Key: instKey})
	waitFor(t, time.Second, func() bool { return sentContains(ff, "/new") })
}

func TestCommands_StatusReportsModelAndState(t *testing.T) {
	_, _, ff, instKey, cancel := newCommandTestCore(t)
	defer cancel()

	ff.Inject(Command{Kind: CmdStatus, Key: instKey})
	waitFor(t, time.Second, func() bool { return sentContains(ff, "anthropic/claude-sonnet-5") })
	waitFor(t, time.Second, func() bool { return sentContains(ff, "queue depth: 0") })
}

// TestCommands_StatusOnSuspendedInstanceSkipsExec is Step 29's edge case:
// a suspended instance reports that directly rather than attempting (and
// confusingly failing) an Exec against a stopped container.
func TestCommands_StatusOnSuspendedInstanceSkipsExec(t *testing.T) {
	_, _, ff, instKey, cancel := newCommandTestCore(t)
	defer cancel()

	ff.Inject(Command{Kind: CmdSuspend, Key: instKey})
	waitFor(t, time.Second, func() bool { return sentContains(ff, "suspended") })

	ff.Inject(Command{Kind: CmdStatus, Key: instKey})
	waitFor(t, time.Second, func() bool { return sentContains(ff, "no cost/session data available while stopped") })
}

// TestCommands_StatusReportsPendingApproval checks a pending approval shows
// up in /status, not just in the original approval_request notice.
func TestCommands_StatusReportsPendingApproval(t *testing.T) {
	_, rt, ff, instKey, cancel := newCommandTestCore(t)
	defer cancel()

	rt.ScriptTurn("px-cmdtest", TurnScript{Hang: true, Events: []subagent.ChildEvent{
		{Type: "approval_request", Command: "rm -rf /tmp/x", Description: "cleanup", Risk: "high"},
	}})
	ff.Inject(Command{Kind: CmdMessage, Text: "go", Key: instKey})
	waitFor(t, time.Second, func() bool { return sentContains(ff, "approval needed") })
	if !sentContains(ff, "cleanup") {
		t.Error("approval notice should include the description")
	}

	ff.Inject(Command{Kind: CmdStatus, Key: instKey})
	waitFor(t, time.Second, func() bool { return sentContains(ff, "awaiting approval (risk high): rm -rf /tmp/x") })
}

func TestCommands_MessageToUnknownInstanceGetsExplicitError(t *testing.T) {
	cfg := testCoreConfig(t)
	rt := NewFakeRuntime().WithStateRoot(cfg.StateRoot)
	ff := NewFakeFrontend()
	core := NewCore(rt, ff, cfg)
	cancel := startCore(t, core)
	defer cancel()
	_ = core

	ff.Inject(Command{Kind: CmdMessage, Text: "hi", Key: ChannelKey{Frontend: "fake", Chat: "fake-chat", Topic: "nonexistent"}})
	waitFor(t, time.Second, func() bool { return sentContains(ff, "no instance here") })
}

// --- small test-only accessors, same package so unexported fields reachable ---

func instanceModel(t *testing.T, core *Core, name string) string {
	t.Helper()
	core.mu.Lock()
	inst, ok := core.byName[name]
	core.mu.Unlock()
	if !ok {
		t.Fatalf("instance %q not found", name)
	}
	return inst.ModelString()
}

func instanceDesiredState(t *testing.T, core *Core, name string) DesiredState {
	t.Helper()
	core.mu.Lock()
	inst, ok := core.byName[name]
	core.mu.Unlock()
	if !ok {
		t.Fatalf("instance %q not found", name)
	}
	return inst.DesiredStateValue()
}

func instancePending(t *testing.T, core *Core, name string) *PendingApproval {
	t.Helper()
	core.mu.Lock()
	inst, ok := core.byName[name]
	core.mu.Unlock()
	if !ok {
		t.Fatalf("instance %q not found", name)
	}
	return inst.Pending()
}
