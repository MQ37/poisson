package orchestrator

import (
	"context"
	"testing"
	"time"
)

// TestReconcile_FinishesInterruptedKill checks a PendingDestroy=true found
// at startup (a previous /kill interrupted mid-flight) gets finished, and
// the instance is never registered afterward.
func TestReconcile_FinishesInterruptedKill(t *testing.T) {
	cfg := testCoreConfig(t)
	rt := NewFakeRuntime().WithStateRoot(cfg.StateRoot)

	if err := rt.Create(context.Background(), InstanceSpec{Name: "px-pending"}); err != nil {
		t.Fatal(err)
	}
	meta := InstanceMeta{Name: "px-pending", SessionID: "s-1", Model: "anthropic/claude-sonnet-5", PendingDestroy: true, DesiredState: DesiredRunning}
	if err := SaveMeta(cfg.StateRoot, meta); err != nil {
		t.Fatal(err)
	}

	ff := NewFakeFrontend()
	core := NewCore(rt, ff, cfg)
	cancel := startCore(t, core)
	defer cancel()

	waitFor(t, time.Second, func() bool {
		infos, _ := rt.List(context.Background())
		return len(infos) == 0
	})
	core.mu.Lock()
	_, registered := core.byName["px-pending"]
	core.mu.Unlock()
	if registered {
		t.Error("an instance with PendingDestroy should never be registered after reconciliation finishes destroying it")
	}
}

// TestReconcile_StartsInstanceMissingEntirely checks metadata says an
// instance should exist (DesiredState running) but the runtime reports it
// missing entirely (e.g. host rebooted before it was ever started) — it
// gets started fresh.
func TestReconcile_StartsInstanceMissingEntirely(t *testing.T) {
	cfg := testCoreConfig(t)
	rt := NewFakeRuntime().WithStateRoot(cfg.StateRoot)

	if err := rt.Create(context.Background(), InstanceSpec{Name: "px-missing"}); err != nil {
		t.Fatal(err)
	}
	// Note: not Started — rt.List will report it as "stopped".
	meta := InstanceMeta{Name: "px-missing", SessionID: "s-1", Model: "anthropic/claude-sonnet-5", Frontend: "fake", ChatID: "fake-chat", TopicID: "1", DesiredState: DesiredRunning}
	if err := SaveMeta(cfg.StateRoot, meta); err != nil {
		t.Fatal(err)
	}

	ff := NewFakeFrontend()
	core := NewCore(rt, ff, cfg)
	cancel := startCore(t, core)
	defer cancel()

	waitFor(t, time.Second, func() bool {
		infos, _ := rt.List(context.Background())
		return len(infos) == 1 && infos[0].State == "running"
	})
	core.mu.Lock()
	_, registered := core.byName["px-missing"]
	core.mu.Unlock()
	if !registered {
		t.Error("instance should be registered after reconciliation starts it")
	}
}

// TestReconcile_StopsRunningInstanceThatShouldBeSuspended checks a
// DesiredState of suspended against a runtime that reports it still
// running gets stopped.
func TestReconcile_StopsRunningInstanceThatShouldBeSuspended(t *testing.T) {
	cfg := testCoreConfig(t)
	rt := NewFakeRuntime().WithStateRoot(cfg.StateRoot)

	ctx := context.Background()
	if err := rt.Create(ctx, InstanceSpec{Name: "px-suspended"}); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(ctx, "px-suspended"); err != nil {
		t.Fatal(err)
	}
	meta := InstanceMeta{Name: "px-suspended", SessionID: "s-1", Model: "anthropic/claude-sonnet-5", Frontend: "fake", ChatID: "fake-chat", TopicID: "1", DesiredState: DesiredSuspended}
	if err := SaveMeta(cfg.StateRoot, meta); err != nil {
		t.Fatal(err)
	}

	ff := NewFakeFrontend()
	core := NewCore(rt, ff, cfg)
	cancel := startCore(t, core)
	defer cancel()

	waitFor(t, time.Second, func() bool {
		infos, _ := rt.List(context.Background())
		return len(infos) == 1 && infos[0].State == "stopped"
	})
}

// TestReconcile_RehydratesCompositeRuntimeKindOfForHostInstance checks the
// real restart scenario RehydrateKindOf exists for: a host-kind instance's
// metadata survives a restart on disk, but a freshly constructed
// CompositeRuntime's kindOf map starts empty — reconcile must rehydrate it
// before Start/StopOrphanedTurn/Destroy are called, or every one of those
// would fail with "unknown instance" for every existing instance on every
// restart.
func TestReconcile_RehydratesCompositeRuntimeKindOfForHostInstance(t *testing.T) {
	cfg := testCoreConfig(t)
	hostFake := NewFakeRuntime().WithStateRoot(cfg.StateRoot)
	if err := hostFake.Create(context.Background(), InstanceSpec{Name: "px-hostinst"}); err != nil {
		t.Fatal(err)
	}
	// Fresh CompositeRuntime, as cmd/px/orchestrate.go builds at every
	// process start — kindOf starts empty regardless of what existed
	// before.
	rt := NewCompositeRuntime(map[string]Runtime{KindBox: NewFakeRuntime(), KindHost: hostFake})

	meta := InstanceMeta{Name: "px-hostinst", SessionID: "s-1", Model: "anthropic/claude-sonnet-5",
		Frontend: "fake", ChatID: "fake-chat", TopicID: "1", DesiredState: DesiredRunning, Kind: KindHost}
	if err := SaveMeta(cfg.StateRoot, meta); err != nil {
		t.Fatal(err)
	}

	ff := NewFakeFrontend()
	core := NewCore(rt, ff, cfg)
	cancel := startCore(t, core)
	defer cancel()

	waitFor(t, time.Second, func() bool {
		infos, _ := hostFake.List(context.Background())
		return len(infos) == 1 && infos[0].State == "running"
	})
	core.mu.Lock()
	_, registered := core.byName["px-hostinst"]
	core.mu.Unlock()
	if !registered {
		t.Error("host instance should be registered after reconciliation")
	}
}

// TestShouldNotifyForBoot_RateLimitsWithinSameBoot is Step 27's crash-loop
// edge case: within the same boot, the second call must return false so a
// Restart=always crash loop doesn't spam the notice on every attempt.
func TestShouldNotifyForBoot_RateLimitsWithinSameBoot(t *testing.T) {
	if currentBootID() == "" {
		t.Skip("no /proc/sys/kernel/random/boot_id on this host — nothing to rate-limit against")
	}
	dir := t.TempDir()
	if !shouldNotifyForBoot(dir) {
		t.Fatal("first call in a fresh state dir should notify")
	}
	if shouldNotifyForBoot(dir) {
		t.Error("second call within the same boot should NOT notify again")
	}
}

// TestReconcile_RunningNotifiesTopicOfInterruption checks the "your
// previous turn was interrupted" notice is posted for a reconciled running
// instance.
func TestReconcile_RunningNotifiesTopicOfInterruption(t *testing.T) {
	cfg := testCoreConfig(t)
	rt := NewFakeRuntime().WithStateRoot(cfg.StateRoot)
	ctx := context.Background()
	if err := rt.Create(ctx, InstanceSpec{Name: "px-notify"}); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(ctx, "px-notify"); err != nil {
		t.Fatal(err)
	}
	meta := InstanceMeta{Name: "px-notify", SessionID: "s-1", Model: "anthropic/claude-sonnet-5", Frontend: "fake", ChatID: "fake-chat", TopicID: "1", DesiredState: DesiredRunning}
	if err := SaveMeta(cfg.StateRoot, meta); err != nil {
		t.Fatal(err)
	}

	ff := NewFakeFrontend()
	core := NewCore(rt, ff, cfg)
	cancel := startCore(t, core)
	defer cancel()

	waitFor(t, time.Second, func() bool { return sentContains(ff, "any previous turn was interrupted") })
}
