package orchestrator

import (
	"context"
	"testing"
)

func TestCompositeRuntime_CreateKindCrossKindNameCollision(t *testing.T) {
	c := NewCompositeRuntime(map[string]Runtime{
		KindBox:  NewFakeRuntime(),
		KindHost: NewFakeRuntime(),
	})
	ctx := context.Background()

	if err := c.CreateKind(ctx, KindBox, InstanceSpec{Name: "px-x"}); err != nil {
		t.Fatalf("CreateKind box: %v", err)
	}
	if err := c.CreateKind(ctx, KindHost, InstanceSpec{Name: "px-x"}); err == nil {
		t.Error("CreateKind host with a name already used by box: want error, got nil (silent overwrite)")
	}
}

func TestCompositeRuntime_UnknownKindRefused(t *testing.T) {
	c := NewCompositeRuntime(map[string]Runtime{KindBox: NewFakeRuntime()})
	if err := c.CreateKind(context.Background(), "bogus", InstanceSpec{Name: "px-x"}); err == nil {
		t.Error("CreateKind with an unregistered kind: want error, got nil")
	}
}

func TestCompositeRuntime_ListConcatenatesBothBackends(t *testing.T) {
	boxRT := NewFakeRuntime()
	hostRT := NewFakeRuntime()
	c := NewCompositeRuntime(map[string]Runtime{KindBox: boxRT, KindHost: hostRT})
	ctx := context.Background()

	if err := c.CreateKind(ctx, KindBox, InstanceSpec{Name: "px-boxone"}); err != nil {
		t.Fatal(err)
	}
	if err := c.CreateKind(ctx, KindHost, InstanceSpec{Name: "px-hostone"}); err != nil {
		t.Fatal(err)
	}

	infos, err := c.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 2 {
		t.Fatalf("List() = %+v, want exactly 2 (one per backend)", infos)
	}
	names := map[string]bool{infos[0].Name: true, infos[1].Name: true}
	if !names["px-boxone"] || !names["px-hostone"] {
		t.Errorf("List() = %+v, want both px-boxone and px-hostone", infos)
	}
}

func TestCompositeRuntime_DestroyRoutesToOwningBackendAndClearsKindOf(t *testing.T) {
	boxRT := NewFakeRuntime()
	hostRT := NewFakeRuntime()
	c := NewCompositeRuntime(map[string]Runtime{KindBox: boxRT, KindHost: hostRT})
	ctx := context.Background()

	if err := c.CreateKind(ctx, KindHost, InstanceSpec{Name: "px-h"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Destroy(ctx, "px-h"); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if infos, _ := hostRT.List(ctx); len(infos) != 0 {
		t.Errorf("hostRT still lists px-h after Destroy: %+v", infos)
	}
	if infos, _ := boxRT.List(ctx); len(infos) != 0 {
		t.Errorf("boxRT unexpectedly touched: %+v", infos)
	}
	// Name is free again under a different kind after Destroy clears kindOf.
	if err := c.CreateKind(ctx, KindBox, InstanceSpec{Name: "px-h"}); err != nil {
		t.Errorf("CreateKind box for a destroyed name: want success, got %v", err)
	}
}

func TestCompositeRuntime_StartStopExecRouteToOwningBackend(t *testing.T) {
	boxRT := NewFakeRuntime()
	hostRT := NewFakeRuntime()
	c := NewCompositeRuntime(map[string]Runtime{KindBox: boxRT, KindHost: hostRT})
	ctx := context.Background()

	if err := c.CreateKind(ctx, KindHost, InstanceSpec{Name: "px-h"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(ctx, "px-h"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	infos, _ := hostRT.List(ctx)
	if len(infos) != 1 || infos[0].State != "running" {
		t.Fatalf("hostRT.List() = %+v, want px-h running", infos)
	}
	if err := c.Stop(ctx, "px-h"); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if _, _, _, err := c.Exec(ctx, "px-h", []string{"true"}); err != nil {
		t.Errorf("Exec: %v", err)
	}
}

// TestCompositeRuntime_RehydrateKindOfRestoresRoutingAfterRestart
// simulates the real failure mode a fresh (post-restart) CompositeRuntime
// would hit: an instance created under one process's kindOf map is
// invisible to a brand-new CompositeRuntime instance until rehydrated from
// persisted metadata.
func TestCompositeRuntime_RehydrateKindOfRestoresRoutingAfterRestart(t *testing.T) {
	hostRT := NewFakeRuntime()
	if err := hostRT.Create(context.Background(), InstanceSpec{Name: "px-h"}); err != nil {
		t.Fatal(err)
	}

	fresh := NewCompositeRuntime(map[string]Runtime{KindBox: NewFakeRuntime(), KindHost: hostRT})
	if err := fresh.Start(context.Background(), "px-h"); err == nil {
		t.Fatal("Start before rehydration: want error (unknown instance), got nil")
	}

	fresh.RehydrateKindOf([]InstanceMeta{{Name: "px-h", Kind: KindHost}})
	if err := fresh.Start(context.Background(), "px-h"); err != nil {
		t.Errorf("Start after rehydration: %v, want nil", err)
	}
}

// TestCompositeRuntime_RehydrateKindOfTreatsEmptyKindAsBox checks the same
// empty-means-box compatibility shim InstanceMeta.Kind's doc comment
// documents, applied at rehydration time.
func TestCompositeRuntime_RehydrateKindOfTreatsEmptyKindAsBox(t *testing.T) {
	boxRT := NewFakeRuntime()
	if err := boxRT.Create(context.Background(), InstanceSpec{Name: "px-legacy"}); err != nil {
		t.Fatal(err)
	}
	c := NewCompositeRuntime(map[string]Runtime{KindBox: boxRT, KindHost: NewFakeRuntime()})
	c.RehydrateKindOf([]InstanceMeta{{Name: "px-legacy", Kind: ""}})
	if err := c.Start(context.Background(), "px-legacy"); err != nil {
		t.Errorf("Start on empty-Kind (pre-upgrade) metadata: %v, want nil (routed to box)", err)
	}
}

func TestCompositeRuntime_UnknownInstanceNameRefused(t *testing.T) {
	c := NewCompositeRuntime(map[string]Runtime{KindBox: NewFakeRuntime()})
	ctx := context.Background()
	if err := c.Start(ctx, "px-nosuch"); err == nil {
		t.Error("Start on an unknown instance: want error, got nil")
	}
	if err := c.Destroy(ctx, "px-nosuch"); err == nil {
		t.Error("Destroy on an unknown instance: want error, got nil")
	}
}
