package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/subagent"
)

func testCoreConfig(t *testing.T) CoreConfig {
	t.Helper()
	return CoreConfig{
		DefaultProvider: "anthropic",
		DefaultModel:    "claude-sonnet-5",
		AllowedModels:   []string{"anthropic/claude-sonnet-5", "xai/grok-build"},
		StateRoot:       t.TempDir(),
		MailboxSize:     8,
	}
}

// waitFor polls cond until it's true or timeout elapses, failing the test
// otherwise — every Core test below drives real goroutines (the actor
// loop, Run's dispatch loop), so a fixed sleep would be both slow and
// flaky; polling is not.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}

func sentContains(ff *FakeFrontend, substr string) bool {
	for _, s := range ff.Sent() {
		if strings.Contains(s.Msg.Text, substr) {
			return true
		}
	}
	return false
}

// startCore launches core.Run in the background and returns a cancel func
// that stops it and waits for Run to actually return.
func startCore(t *testing.T, core *Core) (cancel func()) {
	t.Helper()
	ctx, cancelCtx := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		core.Run(ctx)
		close(runDone)
	}()
	ff := core.fe.(*FakeFrontend)
	waitFor(t, time.Second, ff.Ready)
	return func() {
		cancelCtx()
		select {
		case <-runDone:
		case <-time.After(2 * time.Second):
			t.Error("core.Run did not return after cancel")
		}
	}
}

var generalKey = ChannelKey{Frontend: "fake", Chat: "fake-chat", Topic: "general"}

func TestCore_NewCreatesInstanceAndRegistersActor(t *testing.T) {
	cfg := testCoreConfig(t)
	rt := NewFakeRuntime().WithStateRoot(cfg.StateRoot)
	ff := NewFakeFrontend()
	core := NewCore(rt, ff, cfg)
	cancel := startCore(t, core)
	defer cancel()

	ff.Inject(Command{Kind: CmdNew, Args: []string{"alpha"}, Key: generalKey})
	waitFor(t, time.Second, func() bool { return sentContains(ff, "created and running") })

	infos, _ := rt.List(context.Background())
	if len(infos) != 1 || infos[0].Name != "px-alpha" {
		t.Fatalf("rt.List() = %+v, want exactly px-alpha running", infos)
	}
}

// TestCore_TurnYoloDependsOnKind checks docs/orchestrator-host-mode-plan.md
// §3.1: a box-kind instance's turns run yolo, a host-kind instance's never
// do, and empty Kind (pre-upgrade metadata) is treated the same as "box" —
// the one genuinely load-bearing compatibility case.
func TestCore_TurnYoloDependsOnKind(t *testing.T) {
	cfg := testCoreConfig(t)
	rt := NewFakeRuntime().WithStateRoot(cfg.StateRoot)
	ff := NewFakeFrontend()
	core := NewCore(rt, ff, cfg)
	cancel := startCore(t, core)
	defer cancel()

	cases := []struct {
		kind     string
		wantYolo bool
	}{
		{KindBox, true},
		{KindHost, false},
		{"", true}, // pre-Kind-field metadata
	}
	for i, c := range cases {
		reqName := fmt.Sprintf("yolo%d", i)
		ff.Inject(Command{Kind: CmdNew, Args: []string{reqName}, Key: generalKey})
		waitFor(t, time.Second, func() bool { return sentContains(ff, "px-"+reqName+" created") })

		name := "px-" + reqName
		inst := instanceByName(t, core, name)
		// Kind is otherwise set-once-at-construction (see Instance's own
		// doc comment) — safe to mutate directly here since the instance
		// is still idle, no turn has touched it yet.
		inst.Meta.Kind = c.kind

		ff.Inject(Command{Kind: CmdMessage, Text: "go", Key: inst.Key})
		waitFor(t, time.Second, func() bool { return rt.StartTurnCallCount() >= i+1 })
		call := rt.StartTurnCallAt(i)
		if call.Spec.Yolo != c.wantYolo {
			t.Errorf("kind %q: Yolo = %v, want %v", c.kind, call.Spec.Yolo, c.wantYolo)
		}
		waitFor(t, time.Second, func() bool { return instanceStatus(t, core, name) == StatusIdle })
	}
}

func TestCore_NewRefusesAtMaxInstances(t *testing.T) {
	cfg := testCoreConfig(t)
	cfg.MaxInstances = 1
	rt := NewFakeRuntime().WithStateRoot(cfg.StateRoot)
	ff := NewFakeFrontend()
	core := NewCore(rt, ff, cfg)
	cancel := startCore(t, core)
	defer cancel()

	ff.Inject(Command{Kind: CmdNew, Args: []string{"one"}, Key: generalKey})
	waitFor(t, time.Second, func() bool { return sentContains(ff, "created and running") })

	ff.Inject(Command{Kind: CmdNew, Args: []string{"two"}, Key: generalKey})
	waitFor(t, time.Second, func() bool { return sentContains(ff, "already at the configured limit") })

	infos, _ := rt.List(context.Background())
	if len(infos) != 1 {
		t.Fatalf("rt.List() = %+v, want still exactly 1 instance", infos)
	}
}

// TestCore_MessagesSerializePerInstance is the Step 19 verify criterion:
// two rapid-fire messages to the same instance produce two turns that run
// strictly in order, never overlapping.
func TestCore_MessagesSerializePerInstance(t *testing.T) {
	cfg := testCoreConfig(t)
	rt := NewFakeRuntime().WithStateRoot(cfg.StateRoot)
	ff := NewFakeFrontend()
	core := NewCore(rt, ff, cfg)
	cancel := startCore(t, core)
	defer cancel()

	ff.Inject(Command{Kind: CmdNew, Args: []string{"serial"}, Key: generalKey})
	waitFor(t, time.Second, func() bool { return sentContains(ff, "created and running") })

	name := "px-serial"
	instKey := ChannelKey{Frontend: "fake", Chat: "fake-chat", Topic: "1"}
	rt.ScriptTurn(name, TurnScript{Hang: true})
	rt.ScriptTurn(name, TurnScript{Events: []subagent.ChildEvent{{Type: "done", Success: true}}})

	ff.Inject(Command{Kind: CmdMessage, Text: "first", Key: instKey})
	ff.Inject(Command{Kind: CmdMessage, Text: "second", Key: instKey})

	waitFor(t, time.Second, func() bool { return rt.StartTurnCallCount() >= 1 })
	time.Sleep(50 * time.Millisecond) // give a buggy implementation time to also start the second turn
	if n := rt.StartTurnCallCount(); n != 1 {
		t.Fatalf("StartTurnCallCount = %d while the first turn is still hung, want exactly 1 (no overlap)", n)
	}

	if err := rt.StopTurn(context.Background(), "fake-turn-"+name); err != nil {
		t.Fatal(err)
	}

	waitFor(t, time.Second, func() bool { return rt.StartTurnCallCount() >= 2 })
	if rt.StartTurnCallAt(0).Spec.Message != "first" || rt.StartTurnCallAt(1).Spec.Message != "second" {
		t.Errorf("turn order = [%+v, %+v], want [first, second] strictly in order", rt.StartTurnCallAt(0), rt.StartTurnCallAt(1))
	}
}

// TestCore_MailboxFullRepliesExplicitly is Step 19's mailbox-full edge case
// — never a silent drop.
func TestCore_MailboxFullRepliesExplicitly(t *testing.T) {
	cfg := testCoreConfig(t)
	cfg.MailboxSize = 1
	rt := NewFakeRuntime().WithStateRoot(cfg.StateRoot)
	ff := NewFakeFrontend()
	core := NewCore(rt, ff, cfg)
	cancel := startCore(t, core)
	defer cancel()

	ff.Inject(Command{Kind: CmdNew, Args: []string{"full"}, Key: generalKey})
	waitFor(t, time.Second, func() bool { return sentContains(ff, "created and running") })

	name := "px-full"
	instKey := ChannelKey{Frontend: "fake", Chat: "fake-chat", Topic: "1"}
	rt.ScriptTurn(name, TurnScript{Hang: true})

	// One message starts the (hung) turn; the mailbox (capacity 1) then
	// absorbs one more; a third must be refused explicitly.
	ff.Inject(Command{Kind: CmdMessage, Text: "one", Key: instKey})
	waitFor(t, time.Second, func() bool { return rt.StartTurnCallCount() >= 1 })
	ff.Inject(Command{Kind: CmdMessage, Text: "two", Key: instKey})
	ff.Inject(Command{Kind: CmdMessage, Text: "three", Key: instKey})

	waitFor(t, time.Second, func() bool { return sentContains(ff, "queue full") })
}

// TestCore_EventBatchingCollapsesSends is the Step 20 verify criterion: a
// scripted sequence of 12 text deltas plus 2 tool-call events plus a final
// done event collapses into a small, bounded number of actual frontend
// calls (batched text + one status message + edits, not one message per
// event — see the tool-call edit-in-place design), ends in idle status,
// and preserves tool-call ordering relative to text.
func TestCore_EventBatchingCollapsesSends(t *testing.T) {
	cfg := testCoreConfig(t)
	rt := NewFakeRuntime().WithStateRoot(cfg.StateRoot)
	ff := NewFakeFrontend()
	core := NewCore(rt, ff, cfg)
	cancel := startCore(t, core)
	defer cancel()

	ff.Inject(Command{Kind: CmdNew, Args: []string{"batch"}, Key: generalKey})
	waitFor(t, time.Second, func() bool { return sentContains(ff, "created and running") })
	baseline := len(ff.Sent())

	name := "px-batch"
	var events []subagent.ChildEvent
	for i := 0; i < 12; i++ {
		events = append(events, subagent.ChildEvent{Type: "text", Text: fmt.Sprintf("chunk%d ", i)})
	}
	events = append(events, subagent.ChildEvent{Type: "tool", Tool: "read"})
	events = append(events, subagent.ChildEvent{Type: "tool", Tool: "bash"})
	events = append(events, subagent.ChildEvent{Type: "done", Success: true})
	rt.ScriptTurn(name, TurnScript{Events: events})

	instKey := ChannelKey{Frontend: "fake", Chat: "fake-chat", Topic: "1"}
	ff.Inject(Command{Kind: CmdMessage, Text: "go", Key: instKey})

	// A fresh instance already starts idle, so waiting for "idle" alone
	// would trivially succeed at t=0 before the turn even ran — wait for
	// the turn to actually start first, then for it to actually finish.
	waitFor(t, time.Second, func() bool { return rt.StartTurnCallCount() >= 1 })
	waitFor(t, time.Second, func() bool {
		return instanceStatus(t, core, name) == StatusIdle
	})

	// 12 text deltas -> 1 batched Send (triggered by the first tool event's
	// flush). 2 tool calls -> 1 Send (first) + 1 Edit (second, in place).
	// done(success:true) -> no extra message. Total: 2 Sends, 1 Edit.
	relevant := ff.Sent()[baseline:]
	if len(relevant) > 3 {
		t.Fatalf("relevant sends = %d (%+v), want at most 3", len(relevant), relevant)
	}
	if len(relevant) != 2 {
		t.Fatalf("relevant sends = %d (%+v), want exactly 2 (batched text + first tool status)", len(relevant), relevant)
	}
	edits := ff.Edits()
	if len(edits) != 1 {
		t.Fatalf("edits = %d (%+v), want exactly 1 (second tool call edited in place)", len(edits), edits)
	}
	if !strings.Contains(relevant[0].Msg.Text, "chunk0") || !strings.Contains(relevant[0].Msg.Text, "chunk11") {
		t.Errorf("send 0 = %q, want the batched text containing chunk0..chunk11", relevant[0].Msg.Text)
	}
	if !strings.Contains(relevant[1].Msg.Text, "read") {
		t.Errorf("send 1 = %q, want the read tool notice (ordering: text before tools)", relevant[1].Msg.Text)
	}
	if !strings.Contains(edits[0].Text, "bash") {
		t.Errorf("edit 0 = %q, want the bash tool notice", edits[0].Text)
	}
}

// TestCore_ToolCallsEditOneStatusMessageInPlace checks multiple tool-call
// events collapse into ONE Send plus N-1 Edit calls against that same
// message, rather than N separate new messages — the fix for a long turn
// otherwise flooding the topic with one line per tool call.
func TestCore_ToolCallsEditOneStatusMessageInPlace(t *testing.T) {
	cfg := testCoreConfig(t)
	rt := NewFakeRuntime().WithStateRoot(cfg.StateRoot)
	ff := NewFakeFrontend()
	core := NewCore(rt, ff, cfg)
	cancel := startCore(t, core)
	defer cancel()

	ff.Inject(Command{Kind: CmdNew, Args: []string{"toolspam"}, Key: generalKey})
	waitFor(t, time.Second, func() bool { return sentContains(ff, "created and running") })
	baseline := len(ff.Sent())

	name := "px-toolspam"
	events := []subagent.ChildEvent{
		{Type: "tool", Tool: "read"},
		{Type: "tool", Tool: "grep"},
		{Type: "tool", Tool: "edit"},
		{Type: "tool", Tool: "bash"},
		{Type: "done", Success: true},
	}
	rt.ScriptTurn(name, TurnScript{Events: events})

	instKey := ChannelKey{Frontend: "fake", Chat: "fake-chat", Topic: "1"}
	ff.Inject(Command{Kind: CmdMessage, Text: "go", Key: instKey})
	waitFor(t, time.Second, func() bool { return rt.StartTurnCallCount() >= 1 })
	waitFor(t, time.Second, func() bool { return instanceStatus(t, core, name) == StatusIdle })

	relevant := ff.Sent()[baseline:]
	if len(relevant) != 1 {
		t.Fatalf("Sent() after baseline = %d (%+v), want exactly 1 (one status message, the rest edited in place)", len(relevant), relevant)
	}
	edits := ff.Edits()
	if len(edits) != 3 {
		t.Fatalf("Edits() = %d (%+v), want exactly 3 (4 tool calls = 1 send + 3 edits)", len(edits), edits)
	}
	if !strings.Contains(edits[len(edits)-1].Text, "bash") || !strings.Contains(edits[len(edits)-1].Text, "4 calls") {
		t.Errorf("last edit = %q, want it to mention bash and a count of 4", edits[len(edits)-1].Text)
	}
	for _, e := range edits {
		if e.MsgID == "" || e.MsgID != edits[0].MsgID {
			t.Errorf("edit %+v targets a different/empty message id, want every edit to target the same status message", e)
		}
	}
}

// TestCore_TurnEndsWithoutDoneDistinguishesKilledFromCrashed is Step 20's
// terminal-synthesis edge case.
func TestCore_TurnEndsWithoutDoneDistinguishesKilledFromCrashed(t *testing.T) {
	cfg := testCoreConfig(t)
	rt := NewFakeRuntime().WithStateRoot(cfg.StateRoot)
	ff := NewFakeFrontend()
	core := NewCore(rt, ff, cfg)
	cancel := startCore(t, core)
	defer cancel()

	ff.Inject(Command{Kind: CmdNew, Args: []string{"crash"}, Key: generalKey})
	waitFor(t, time.Second, func() bool { return sentContains(ff, "created and running") })

	name := "px-crash"
	instKey := ChannelKey{Frontend: "fake", Chat: "fake-chat", Topic: "1"}

	// A turn that just ends (EOF, no "done") with no deliberate stop from
	// this package — the "crashed" branch.
	rt.ScriptTurn(name, TurnScript{Events: nil})
	ff.Inject(Command{Kind: CmdMessage, Text: "go", Key: instKey})
	waitFor(t, time.Second, func() bool { return sentContains(ff, "ended unexpectedly") })

	// A turn deliberately stopped via /cancel — the "stopped" branch.
	rt.ScriptTurn(name, TurnScript{Hang: true})
	ff.Inject(Command{Kind: CmdMessage, Text: "go again", Key: instKey})
	waitFor(t, time.Second, func() bool { return rt.StartTurnCallCount() >= 2 })
	ff.Inject(Command{Kind: CmdCancel, Key: instKey})
	waitFor(t, time.Second, func() bool { return sentContains(ff, "turn stopped") })
}

func instanceByName(t *testing.T, core *Core, name string) *Instance {
	t.Helper()
	core.mu.Lock()
	defer core.mu.Unlock()
	inst, ok := core.byName[name]
	if !ok {
		t.Fatalf("instance %q not registered", name)
	}
	return inst
}

func instanceStatus(t *testing.T, core *Core, name string) Status {
	t.Helper()
	core.mu.Lock()
	defer core.mu.Unlock()
	inst, ok := core.byName[name]
	if !ok {
		t.Fatalf("instance %q not registered", name)
	}
	return inst.Status()
}
