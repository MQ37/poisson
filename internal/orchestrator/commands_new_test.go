package orchestrator

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestHandleNew_PlainNewUnchanged is the explicit regression guard the plan
// calls for: bare /new (CmdNew, an alias for CmdNewBox) must keep behaving
// exactly as before host mode existed.
func TestHandleNew_PlainNewUnchanged(t *testing.T) {
	cfg := testCoreConfig(t)
	rt := NewFakeRuntime().WithStateRoot(cfg.StateRoot)
	ff := NewFakeFrontend()
	core := NewCore(rt, ff, cfg)
	cancel := startCore(t, core)
	defer cancel()

	ff.Inject(Command{Kind: CmdNew, Args: []string{"plain"}, Key: generalKey})
	waitFor(t, time.Second, func() bool { return sentContains(ff, "px-plain created and running") })

	inst := instanceByName(t, core, "px-plain")
	if inst.Meta.Kind != KindBox {
		t.Errorf("plain /new Kind = %q, want %q", inst.Meta.Kind, KindBox)
	}
}

func TestHandleNew_NewBoxSucceeds(t *testing.T) {
	cfg := testCoreConfig(t)
	rt := NewFakeRuntime().WithStateRoot(cfg.StateRoot)
	ff := NewFakeFrontend()
	core := NewCore(rt, ff, cfg)
	cancel := startCore(t, core)
	defer cancel()

	ff.Inject(Command{Kind: CmdNewBox, Args: []string{"boxed"}, Key: generalKey})
	waitFor(t, time.Second, func() bool { return sentContains(ff, "px-boxed created and running") })
}

func TestHandleNew_HostRefusedWhenDisabled(t *testing.T) {
	cfg := testCoreConfig(t)
	// AllowHostInstances left false (zero value / default).
	boxRT := NewFakeRuntime().WithStateRoot(cfg.StateRoot)
	rt := NewCompositeRuntime(map[string]Runtime{KindBox: boxRT, KindHost: NewFakeRuntime().WithStateRoot(cfg.StateRoot)})
	ff := NewFakeFrontend()
	core := NewCore(rt, ff, cfg)
	cancel := startCore(t, core)
	defer cancel()

	ff.Inject(Command{Kind: CmdNewHost, Args: []string{"hostname", hostConfirmFlag}, Key: generalKey})
	waitFor(t, time.Second, func() bool { return sentContains(ff, "host instances are disabled") })

	if infos, _ := rt.List(context.Background()); len(infos) != 0 {
		t.Errorf("rt.List() = %+v, want nothing created", infos)
	}
}

func TestHandleNew_HostWithoutConfirmationFlagCreatesNothing(t *testing.T) {
	cfg := testCoreConfig(t)
	cfg.AllowHostInstances = true
	boxRT := NewFakeRuntime().WithStateRoot(cfg.StateRoot)
	rt := NewCompositeRuntime(map[string]Runtime{KindBox: boxRT, KindHost: NewFakeRuntime().WithStateRoot(cfg.StateRoot)})
	ff := NewFakeFrontend()
	core := NewCore(rt, ff, cfg)
	cancel := startCore(t, core)
	defer cancel()

	ff.Inject(Command{Kind: CmdNewHost, Args: []string{"hostname"}, Key: generalKey})
	waitFor(t, time.Second, func() bool { return sentContains(ff, "/new-host hostname "+hostConfirmFlag) })

	if infos, _ := rt.List(context.Background()); len(infos) != 0 {
		t.Errorf("rt.List() = %+v, want nothing created", infos)
	}
}

func TestHandleNew_HostWithConfirmationFlagSucceedsWhenEnabled(t *testing.T) {
	cfg := testCoreConfig(t)
	cfg.AllowHostInstances = true
	boxRT := NewFakeRuntime().WithStateRoot(cfg.StateRoot)
	rt := NewCompositeRuntime(map[string]Runtime{KindBox: boxRT, KindHost: NewFakeRuntime().WithStateRoot(cfg.StateRoot)})
	ff := NewFakeFrontend()
	core := NewCore(rt, ff, cfg)
	cancel := startCore(t, core)
	defer cancel()

	ff.Inject(Command{Kind: CmdNewHost, Args: []string{"hostname", hostConfirmFlag}, Key: generalKey})
	waitFor(t, time.Second, func() bool { return sentContains(ff, "px-hostname created and running") })

	inst := instanceByName(t, core, "px-hostname")
	if inst.Meta.Kind != KindHost {
		t.Errorf("Kind = %q, want %q", inst.Meta.Kind, KindHost)
	}
}

// TestHandleNew_HostRefusedAtOwnCapIndependentOfBoxCap checks MaxHostInstances
// is a completely separate ceiling from MaxInstances (box-only) — hitting it
// never mentions or is affected by the box count.
func TestHandleNew_HostRefusedAtOwnCapIndependentOfBoxCap(t *testing.T) {
	cfg := testCoreConfig(t)
	cfg.AllowHostInstances = true
	cfg.MaxHostInstances = 1
	cfg.MaxInstances = 1
	boxRT := NewFakeRuntime().WithStateRoot(cfg.StateRoot)
	rt := NewCompositeRuntime(map[string]Runtime{KindBox: boxRT, KindHost: NewFakeRuntime().WithStateRoot(cfg.StateRoot)})
	ff := NewFakeFrontend()
	core := NewCore(rt, ff, cfg)
	cancel := startCore(t, core)
	defer cancel()

	// A box instance at its own (separate) cap must not affect host's cap.
	ff.Inject(Command{Kind: CmdNewBox, Args: []string{"onebox"}, Key: generalKey})
	waitFor(t, time.Second, func() bool { return sentContains(ff, "px-onebox created and running") })

	ff.Inject(Command{Kind: CmdNewHost, Args: []string{"onehost", hostConfirmFlag}, Key: generalKey})
	waitFor(t, time.Second, func() bool { return sentContains(ff, "px-onehost created and running") })

	ff.Inject(Command{Kind: CmdNewHost, Args: []string{"twohost", hostConfirmFlag}, Key: generalKey})
	waitFor(t, time.Second, func() bool { return sentContains(ff, "already at the configured limit of 1 host instances") })

	if infos, _ := rt.List(context.Background()); len(infos) != 2 {
		t.Fatalf("rt.List() = %+v, want exactly 2 (one box, one host)", infos)
	}
}

// TestHandleNew_HostConfirmationCheckedBeforeAllowGateNeverLeaks checks the
// disabled-gate error fires even with the confirmation flag present, and
// never reveals the confirmation incantation to a caller who can't use it
// anyway.
func TestHandleNew_HostConfirmationCheckedBeforeAllowGateNeverLeaks(t *testing.T) {
	cfg := testCoreConfig(t)
	rt := NewCompositeRuntime(map[string]Runtime{KindBox: NewFakeRuntime().WithStateRoot(cfg.StateRoot), KindHost: NewFakeRuntime().WithStateRoot(cfg.StateRoot)})
	ff := NewFakeFrontend()
	core := NewCore(rt, ff, cfg)
	cancel := startCore(t, core)
	defer cancel()

	ff.Inject(Command{Kind: CmdNewHost, Args: []string{"hostname", hostConfirmFlag}, Key: generalKey})
	waitFor(t, time.Second, func() bool { return sentContains(ff, "host instances are disabled") })

	for _, s := range ff.Sent() {
		if strings.Contains(s.Msg.Text, hostConfirmFlag) {
			t.Errorf("disabled-gate reply leaked the confirmation flag: %q", s.Msg.Text)
		}
	}
}
