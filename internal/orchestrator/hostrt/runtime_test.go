package hostrt

import (
	"context"
	"testing"

	"github.com/mq37/poisson/internal/orchestrator"
)

func testRuntime(t *testing.T) *Runtime {
	t.Helper()
	return NewRuntime(Config{PxBinPath: "/usr/local/bin/px", StateRoot: t.TempDir()})
}

func TestRuntime_CreateStartStopDestroyIsIdempotent(t *testing.T) {
	r := testRuntime(t)
	ctx := context.Background()
	spec := orchestrator.InstanceSpec{Name: "px-alpha"}

	if err := r.Create(ctx, spec); err != nil {
		t.Fatalf("Create: %v", err)
	}
	infos, err := r.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].Name != "px-alpha" || infos[0].State != "stopped" {
		t.Fatalf("List() after Create = %+v, want exactly px-alpha stopped", infos)
	}

	if err := r.Start(ctx, "px-alpha"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	infos, _ = r.List(ctx)
	if len(infos) != 1 || infos[0].State != "running" {
		t.Fatalf("List() after Start = %+v, want running", infos)
	}
	// Idempotent.
	if err := r.Start(ctx, "px-alpha"); err != nil {
		t.Errorf("second Start = %v, want nil (idempotent)", err)
	}

	if err := r.Stop(ctx, "px-alpha"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	infos, _ = r.List(ctx)
	if len(infos) != 1 || infos[0].State != "stopped" {
		t.Fatalf("List() after Stop = %+v, want stopped", infos)
	}

	if err := r.Destroy(ctx, "px-alpha"); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	infos, _ = r.List(ctx)
	if len(infos) != 0 {
		t.Errorf("List() after Destroy = %+v, want empty", infos)
	}
	// Idempotent.
	if err := r.Destroy(ctx, "px-alpha"); err != nil {
		t.Errorf("second Destroy = %v, want nil (idempotent)", err)
	}
}

// TestRuntime_ListIgnoresNonHostInstanceDirs checks List doesn't pick up a
// plain instance directory that isn't a host instance (e.g. one a box
// instance's own nspawn.Runtime.Create built via the same
// CreateInstanceLayout, sharing the same StateRoot/instances parent) --
// the marker-file distinction is what List relies on, not directory
// presence alone.
func TestRuntime_ListIgnoresNonHostInstanceDirs(t *testing.T) {
	r := testRuntime(t)
	ctx := context.Background()

	// Build a bare instance layout with no host-state marker, exactly what
	// nspawn.Runtime.Create leaves behind for a box instance.
	if err := orchestrator.CreateInstanceLayout(r.cfg.StateRoot, "px-boxinstance"); err != nil {
		t.Fatal(err)
	}
	if err := r.Create(ctx, orchestrator.InstanceSpec{Name: "px-hostinstance"}); err != nil {
		t.Fatal(err)
	}

	infos, err := r.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].Name != "px-hostinstance" {
		t.Errorf("List() = %+v, want exactly px-hostinstance (box instance dir excluded)", infos)
	}
}

// TestRuntime_StartTurnRefusesUnknownOrSuspended checks StartTurn's
// explicit refusal paths -- host mode has no container whose absence would
// otherwise make this fail implicitly (contrast nspawn).
func TestRuntime_StartTurnRefusesUnknownOrSuspended(t *testing.T) {
	r := testRuntime(t)
	ctx := context.Background()

	if _, err := r.StartTurn(ctx, "px-nosuchinstance", orchestrator.TurnSpec{Message: "hi"}); err == nil {
		t.Error("StartTurn against an unknown instance: want error, got nil")
	}

	if err := r.Create(ctx, orchestrator.InstanceSpec{Name: "px-alpha"}); err != nil {
		t.Fatal(err)
	}
	// Created but never Started -- state is "stopped".
	if _, err := r.StartTurn(ctx, "px-alpha", orchestrator.TurnSpec{Message: "hi"}); err == nil {
		t.Error("StartTurn against a suspended (never-started) instance: want error, got nil")
	}
}

func TestRuntime_CreateRejectsInvalidName(t *testing.T) {
	r := testRuntime(t)
	if err := r.Create(context.Background(), orchestrator.InstanceSpec{Name: "not valid"}); err == nil {
		t.Error("Create with an invalid name: want error, got nil")
	}
}
