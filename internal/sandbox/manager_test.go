package sandbox

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestManager_CreateExecKill(t *testing.T) {
	m := NewManager(NewFakeDriver())
	ctx := context.Background()

	sb, err := m.Create(ctx, CreateOpts{Image: "ubuntu:26.04", HostPath: "/tmp/x/workspace"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sb.ID == "" {
		t.Fatal("Create returned empty ID")
	}
	if sb.HostPath != "/tmp/x/workspace" {
		t.Errorf("HostPath = %q, want /tmp/x/workspace", sb.HostPath)
	}
	if sb.CreatedAt.IsZero() || sb.LastUsed.IsZero() {
		t.Error("CreatedAt/LastUsed should be set")
	}

	stdout, _, exitCode, err := m.Exec(ctx, sb.ID, "echo hi", "", time.Second)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if exitCode != 0 {
		t.Errorf("exitCode = %d, want 0", exitCode)
	}
	if strings.TrimSpace(stdout) != "echo hi" {
		t.Errorf("stdout = %q, want the fake driver's echo", stdout)
	}

	alive, err := m.Alive(ctx, sb.ID)
	if err != nil || !alive {
		t.Fatalf("Alive = %v, %v, want true, nil", alive, err)
	}

	if err := m.Kill(ctx, sb.ID); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if m.Owns(sb.ID) {
		t.Error("Owns still true after Kill — ownership record should be dropped")
	}
}

// TestManager_ExecUpdatesLastUsed confirms a real call moves LastUsed
// forward — used later by idle-reap logic to decide what's gone stale.
func TestManager_ExecUpdatesLastUsed(t *testing.T) {
	m := NewManager(NewFakeDriver())
	ctx := context.Background()
	sb, err := m.Create(ctx, CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	first := sb.LastUsed
	time.Sleep(2 * time.Millisecond)
	if _, _, _, err := m.Exec(ctx, sb.ID, "true", "", time.Second); err != nil {
		t.Fatal(err)
	}
	got, _ := m.Get(sb.ID)
	if !got.LastUsed.After(first) {
		t.Errorf("LastUsed did not advance: before=%v after=%v", first, got.LastUsed)
	}
}

// TestManager_OwnsRejectsForeignID is the core validation guarantee: a
// sandboxId this Manager never created (hallucinated by the model, or
// belonging to a different session entirely) must be rejected by Owns, and
// every operation keyed on it must fail closed before reaching the Driver.
func TestManager_OwnsRejectsForeignID(t *testing.T) {
	m := NewManager(NewFakeDriver())
	ctx := context.Background()

	if m.Owns("fake-999") {
		t.Fatal("Owns true for an id never created by this Manager")
	}
	if _, _, _, err := m.Exec(ctx, "fake-999", "echo hi", "", time.Second); err == nil {
		t.Fatal("Exec on a foreign id should fail")
	}
	if err := m.Kill(ctx, "fake-999"); err == nil {
		t.Fatal("Kill on a foreign id should fail")
	}
	if _, err := m.Alive(ctx, "fake-999"); err == nil {
		t.Fatal("Alive on a foreign id should fail")
	}
}

// TestManager_ForeignSandboxNotConfusedWithAnothersOwn verifies one
// Manager's real, live sandbox is still rejected by a second, independent
// Manager that never created or Authorize'd it — the case of a session
// guessing another session's real container id, not just a made-up string.
func TestManager_ForeignSandboxNotConfusedWithAnothersOwn(t *testing.T) {
	driver := NewFakeDriver() // shared driver, as if both processes reach the same podman
	ctx := context.Background()

	owner := NewManager(driver)
	sb, err := owner.Create(ctx, CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	stranger := NewManager(driver)
	if stranger.Owns(sb.ID) {
		t.Fatal("a second Manager must not own a sandbox it neither created nor was Authorize'd for")
	}
	if _, _, _, err := stranger.Exec(ctx, sb.ID, "echo hi", "", time.Second); err == nil {
		t.Fatal("stranger Manager must not be able to Exec into another Manager's sandbox")
	}
}

// TestManager_Authorize covers the subagent allow-list mechanism: a child
// process's own Manager never called Create, but the parent hands it the
// Sandbox record directly (see POISSON_SUBAGENT_SANDBOXES in
// docs/sandbox-plan.md) — Authorize is how that record enters the child's
// ownership set.
func TestManager_Authorize(t *testing.T) {
	driver := NewFakeDriver()
	ctx := context.Background()

	parent := NewManager(driver)
	sb, err := parent.Create(ctx, CreateOpts{HostPath: "/tmp/shared/workspace"})
	if err != nil {
		t.Fatal(err)
	}

	child := NewManager(driver)
	if child.Owns(sb.ID) {
		t.Fatal("child must not own the parent's sandbox before Authorize")
	}
	child.Authorize(sb)
	if !child.Owns(sb.ID) {
		t.Fatal("child should own the sandbox after Authorize")
	}
	if _, _, _, err := child.Exec(ctx, sb.ID, "echo hi", "", time.Second); err != nil {
		t.Fatalf("child Exec after Authorize: %v", err)
	}
}

// TestManager_AuthorizedSandboxCanGoDeadUnderneathAChild is the liveness
// check a subagent must run before trusting a parent-authorized id: the
// parent (or an idle reaper) may kill the real container after the child
// was told about it, without the child's own ownership record knowing.
// Owns stays true (ownership and liveness are different questions); Alive
// must go false and Exec must fail once the container is actually gone.
func TestManager_AuthorizedSandboxCanGoDeadUnderneathAChild(t *testing.T) {
	driver := NewFakeDriver()
	ctx := context.Background()

	parent := NewManager(driver)
	sb, err := parent.Create(ctx, CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	child := NewManager(driver)
	child.Authorize(sb)

	// Simulate the parent's idle-reap sweep killing the container out from
	// under the child, between authorization and use.
	driver.MarkDead(sb.ID)

	if !child.Owns(sb.ID) {
		t.Fatal("Owns should still be true — ownership and liveness are separate")
	}
	alive, err := child.Alive(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Alive: %v", err)
	}
	if alive {
		t.Fatal("Alive should be false once the container is actually dead")
	}
	if _, _, _, err := child.Exec(ctx, sb.ID, "echo hi", "", time.Second); err == nil {
		t.Fatal("Exec against a dead container should fail, not silently succeed")
	}
}

// TestManager_KillRejectsAlreadyKilled confirms killing twice fails the
// second time cleanly instead of panicking or double-freeing state.
func TestManager_KillRejectsAlreadyKilled(t *testing.T) {
	m := NewManager(NewFakeDriver())
	ctx := context.Background()
	sb, err := m.Create(ctx, CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Kill(ctx, sb.ID); err != nil {
		t.Fatalf("first Kill: %v", err)
	}
	if err := m.Kill(ctx, sb.ID); err == nil {
		t.Fatal("second Kill on an already-killed sandbox should fail")
	}
}

// TestManager_ConcurrentGetAndExecDoNotRace guards Get/Create/Authorize
// returning value copies instead of a live pointer into the tracked map:
// without that, a goroutine reading LastUsed via Get while another calls
// Exec (which mutates it under m.mu) would race the moment bash's
// sandboxId routing dispatches concurrent batch calls against the same id.
// Run with -race to mean anything.
func TestManager_ConcurrentGetAndExecDoNotRace(t *testing.T) {
	m := NewManager(NewFakeDriver())
	ctx := context.Background()
	sb, err := m.Create(ctx, CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			m.Exec(ctx, sb.ID, "echo hi", "", time.Second)
		}
	}()
	for i := 0; i < 50; i++ {
		if _, ok := m.Get(sb.ID); !ok {
			t.Error("Get lost the sandbox mid-race")
		}
	}
	<-done
}

// TestManager_ExecScriptedFailure exercises FakeDriver.ExecFn — a test can
// script a specific exit code/stderr instead of the default echo, e.g. to
// simulate a command failing inside the sandbox.
func TestManager_ExecScriptedFailure(t *testing.T) {
	driver := NewFakeDriver()
	driver.ExecFn = func(_ context.Context, _, cmd, _ string) (string, string, int, error) {
		if cmd == "false" {
			return "", "boom", 1, nil
		}
		return "", "", 0, nil
	}
	m := NewManager(driver)
	ctx := context.Background()
	sb, err := m.Create(ctx, CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	_, stderr, exitCode, err := m.Exec(ctx, sb.ID, "false", "", time.Second)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if exitCode != 1 || stderr != "boom" {
		t.Errorf("exitCode=%d stderr=%q, want 1, \"boom\"", exitCode, stderr)
	}
}
