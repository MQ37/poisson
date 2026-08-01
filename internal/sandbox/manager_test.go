package sandbox

import (
	"context"
	"fmt"
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

// TestFakeDriver_CreateCallsRecorded confirms a caller can inspect exactly
// what CreateOpts a driver received, without a real podman backend —
// needed by callers like a create_sandbox tool that must be tested against
// what it actually passed through (image, mounts, env).
func TestFakeDriver_CreateCallsRecorded(t *testing.T) {
	driver := NewFakeDriver()
	ctx := context.Background()
	opts := CreateOpts{Image: "ubuntu:26.04", HostPath: "/tmp/x/workspace", Env: []string{"A=1"}}
	if _, err := driver.Create(ctx, opts); err != nil {
		t.Fatal(err)
	}
	if len(driver.CreateCalls) != 1 {
		t.Fatalf("CreateCalls = %d entries, want 1", len(driver.CreateCalls))
	}
	if driver.CreateCalls[0].Image != "ubuntu:26.04" || driver.CreateCalls[0].Env[0] != "A=1" {
		t.Errorf("CreateCalls[0] = %+v, did not record what was passed", driver.CreateCalls[0])
	}
}

// TestFakeDriver_CreateErr confirms a scripted failure actually fails
// Create (and thus Manager.Create), not just gets silently ignored.
func TestFakeDriver_CreateErr(t *testing.T) {
	driver := NewFakeDriver()
	driver.CreateErr = fmt.Errorf("boom")
	m := NewManager(driver)
	if _, err := m.Create(context.Background(), CreateOpts{}); err == nil {
		t.Fatal("expected Create to fail when the driver's CreateErr is set")
	}
}

// TestFakeDriver_Start confirms Start flips a MarkDead'd container back to
// alive (idempotent — also succeeds on an already-alive one) and errors for
// an id that was never created at all.
func TestFakeDriver_Start(t *testing.T) {
	driver := NewFakeDriver()
	ctx := context.Background()
	id, err := driver.Create(ctx, CreateOpts{Name: "start-target"})
	if err != nil {
		t.Fatal(err)
	}
	driver.MarkDead(id)

	if err := driver.Start(ctx, id); err != nil {
		t.Fatalf("Start on a dead container: %v", err)
	}
	if alive, _ := driver.Inspect(ctx, id); !alive {
		t.Error("container should be alive after Start")
	}
	if err := driver.Start(ctx, id); err != nil {
		t.Errorf("Start on an already-alive container should be idempotent, got: %v", err)
	}
	if err := driver.Start(ctx, "never-existed"); err == nil {
		t.Error("expected Start to fail for a container that was never created")
	}
}

// TestManager_DiscoveryAttachesForeignLiveSandbox is the crash-recovery
// case: a second, independent Manager (as if a fresh px process after a
// crash, or a different session) never created or Authorize'd the sandbox,
// but with EnableDiscovery it can still find and use it by exact name,
// since podman itself (the shared driver, here) is the real record of what
// exists — not either Manager's own memory.
func TestManager_DiscoveryAttachesForeignLiveSandbox(t *testing.T) {
	driver := NewFakeDriver()
	ctx := context.Background()

	owner := NewManager(driver)
	sb, err := owner.Create(ctx, CreateOpts{Name: "api-testing", HostPath: "/tmp/x/workspace"})
	if err != nil {
		t.Fatal(err)
	}

	fresh := NewManager(driver) // simulates a brand-new process
	if fresh.Owns(sb.ID) {
		t.Fatal("Owns true before EnableDiscovery — discovery must default off")
	}
	fresh.EnableDiscovery()
	if !fresh.Owns(sb.ID) {
		t.Fatal("Owns should discover a live sandbox by name once EnableDiscovery is called")
	}
	got, ok := fresh.Get(sb.ID)
	if !ok || got.HostPath != "/tmp/x/workspace" {
		t.Errorf("Get after discovery = %+v, %v, want matching HostPath", got, ok)
	}
	if _, _, _, err := fresh.Exec(ctx, sb.ID, "echo hi", "", time.Second); err != nil {
		t.Fatalf("Exec after discovery: %v", err)
	}
}

// TestManager_DiscoveryRefusesDeadSandbox confirms a non-running match is
// not silently adopted — a crashed/stopped container is not a usable
// sandbox even if podman still remembers it.
func TestManager_DiscoveryRefusesDeadSandbox(t *testing.T) {
	driver := NewFakeDriver()
	ctx := context.Background()

	owner := NewManager(driver)
	sb, err := owner.Create(ctx, CreateOpts{Name: "dead-one"})
	if err != nil {
		t.Fatal(err)
	}
	driver.MarkDead(sb.ID)

	fresh := NewManager(driver)
	fresh.EnableDiscovery()
	if fresh.Owns(sb.ID) {
		t.Fatal("discovery must not adopt a sandbox that exists but isn't running")
	}
}

// TestManager_DiscoverySubagentManagerNeverEnabled locks in the security
// property the whole design depends on: a subagent's own Manager (built the
// same plain way as any other, via NewManager+Authorize, discovery never
// turned on) must not be able to reach a sandbox it wasn't explicitly
// Authorize'd for, even though the underlying live container is right there
// in the shared driver.
func TestManager_DiscoverySubagentManagerNeverEnabled(t *testing.T) {
	driver := NewFakeDriver()
	ctx := context.Background()

	parent := NewManager(driver)
	sb, err := parent.Create(ctx, CreateOpts{Name: "parents-sandbox"})
	if err != nil {
		t.Fatal(err)
	}

	child := NewManager(driver) // subagent's own Manager: no EnableDiscovery, ever
	if child.Owns(sb.ID) {
		t.Fatal("a subagent Manager must never auto-discover an unauthorized sandbox")
	}
}

// TestManager_ListProxiesDriver confirms Manager.List is a plain pass-
// through to the Driver — browsing is available regardless of discovery,
// since it grants no access on its own (Owns/Get still gate everything).
func TestManager_ListProxiesDriver(t *testing.T) {
	driver := NewFakeDriver()
	ctx := context.Background()
	m := NewManager(driver)
	if _, err := m.Create(ctx, CreateOpts{Name: "listed-one"}); err != nil {
		t.Fatal(err)
	}
	infos, err := m.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].ID != "px-sandbox-listed-one" {
		t.Errorf("List() = %+v, want one entry named px-sandbox-listed-one", infos)
	}
}

// TestManager_ResurrectOwnedStopped covers the same-process case: a Manager
// stops (MarkDead) its own, already-owned sandbox, then resurrects it — no
// discovery needed at all, since it was already in this Manager's local
// map.
func TestManager_ResurrectOwnedStopped(t *testing.T) {
	driver := NewFakeDriver()
	ctx := context.Background()
	m := NewManager(driver)

	sb, err := m.Create(ctx, CreateOpts{Name: "resurrect-owned", HostPath: "/tmp/x/workspace"})
	if err != nil {
		t.Fatal(err)
	}
	driver.MarkDead(sb.ID)

	if alive, _ := m.Alive(ctx, sb.ID); alive {
		t.Fatal("sandbox should be reported dead after MarkDead")
	}

	got, err := m.Resurrect(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Resurrect: %v", err)
	}
	if got.ID != sb.ID || got.HostPath != "/tmp/x/workspace" {
		t.Errorf("Resurrect() = %+v, want matching ID/HostPath", got)
	}
	if alive, err := m.Alive(ctx, sb.ID); err != nil || !alive {
		t.Errorf("Alive after Resurrect = %v, %v, want true, nil", alive, err)
	}
}

// TestManager_ResurrectDiscoveredStopped is the crash-recovery case: a
// fresh Manager (as if a restarted px process) never created or
// Authorize'd the sandbox, but with EnableDiscovery it can still find and
// resurrect a STOPPED one by exact name — attach()'s own running-only
// filter must not apply here, since resurrecting a stopped one is the
// whole point.
func TestManager_ResurrectDiscoveredStopped(t *testing.T) {
	driver := NewFakeDriver()
	ctx := context.Background()

	owner := NewManager(driver)
	sb, err := owner.Create(ctx, CreateOpts{Name: "resurrect-discovered", HostPath: "/tmp/y/workspace"})
	if err != nil {
		t.Fatal(err)
	}
	driver.MarkDead(sb.ID)

	fresh := NewManager(driver) // simulates a brand-new process after restart
	fresh.EnableDiscovery()

	if fresh.Owns(sb.ID) {
		t.Fatal("Owns should still be false for a stopped sandbox before Resurrect — attach() must not adopt a non-running match")
	}

	got, err := fresh.Resurrect(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Resurrect: %v", err)
	}
	if got.ID != sb.ID || got.HostPath != "/tmp/y/workspace" {
		t.Errorf("Resurrect() = %+v, want matching ID/HostPath", got)
	}
	if !fresh.Owns(sb.ID) {
		t.Error("fresh Manager should own the sandbox after successfully resurrecting it")
	}
	if _, _, _, err := fresh.Exec(ctx, sb.ID, "echo hi", "", time.Second); err != nil {
		t.Fatalf("Exec after Resurrect: %v", err)
	}
}

// TestManager_ResurrectRefusedWithoutDiscovery locks in the same security
// boundary Resurrect must extend from attach/Owns: a Manager that never
// created or was Authorize'd for id, and never enabled discovery (e.g. a
// subagent's), must not be able to resurrect it either.
func TestManager_ResurrectRefusedWithoutDiscovery(t *testing.T) {
	driver := NewFakeDriver()
	ctx := context.Background()

	owner := NewManager(driver)
	sb, err := owner.Create(ctx, CreateOpts{Name: "unauthorized-target"})
	if err != nil {
		t.Fatal(err)
	}
	driver.MarkDead(sb.ID)

	stranger := NewManager(driver) // discovery never enabled
	if _, err := stranger.Resurrect(ctx, sb.ID); err == nil {
		t.Fatal("expected Resurrect to refuse a sandbox this Manager wasn't Authorize'd for and has no discovery")
	}
}

// TestManager_ResurrectUnknownIDErrors confirms a nonexistent id (never
// created by anyone) errors clearly rather than somehow starting a
// container that isn't there.
func TestManager_ResurrectUnknownIDErrors(t *testing.T) {
	driver := NewFakeDriver()
	ctx := context.Background()
	m := NewManager(driver)
	m.EnableDiscovery()

	if _, err := m.Resurrect(ctx, "px-sandbox-never-existed"); err == nil {
		t.Fatal("expected Resurrect to fail for an id that was never created")
	}
}

// TestManager_DiagnoseStoppedRequiresDiscovery confirms DiagnoseStopped
// never reports anything for a Manager with discovery off — a subagent
// Manager must not learn a foreign sandbox exists at all, stopped or not.
func TestManager_DiagnoseStoppedRequiresDiscovery(t *testing.T) {
	driver := NewFakeDriver()
	ctx := context.Background()

	owner := NewManager(driver)
	sb, err := owner.Create(ctx, CreateOpts{Name: "diagnose-target"})
	if err != nil {
		t.Fatal(err)
	}
	driver.MarkDead(sb.ID)

	stranger := NewManager(driver)
	if stranger.DiagnoseStopped(ctx, sb.ID) {
		t.Error("DiagnoseStopped should be false without discovery enabled")
	}

	stranger.EnableDiscovery()
	if !stranger.DiagnoseStopped(ctx, sb.ID) {
		t.Error("DiagnoseStopped should be true once discovery is enabled and the sandbox is stopped")
	}
}

// TestResolveSandboxName covers the naming policy every Driver shares:
// prefixed+sanitized when a name is requested, a random prefixed fallback
// when not.
func TestResolveSandboxName(t *testing.T) {
	got, err := ResolveSandboxName("API Testing 2!")
	if err != nil {
		t.Fatal(err)
	}
	if got != "px-sandbox-api-testing-2" {
		t.Errorf("ResolveSandboxName(%q) = %q, want px-sandbox-api-testing-2", "API Testing 2!", got)
	}

	empty, err := ResolveSandboxName("")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(empty, "px-sandbox-") || empty == "px-sandbox-" {
		t.Errorf("ResolveSandboxName(\"\") = %q, want a non-empty random px-sandbox-* name", empty)
	}

	a, _ := ResolveSandboxName("")
	b, _ := ResolveSandboxName("")
	if a == b {
		t.Error("two empty-name resolutions produced the same fallback name — collision risk")
	}
}

// TestManager_CreateNameCollisionErrorsClearly confirms a second
// create_sandbox call reusing a live name fails with a clear error instead
// of silently reusing (or corrupting) the first sandbox's record — the
// agent needs to see this to pick another name or list existing ones.
func TestManager_CreateNameCollisionErrorsClearly(t *testing.T) {
	driver := NewFakeDriver()
	ctx := context.Background()
	m := NewManager(driver)
	if _, err := m.Create(ctx, CreateOpts{Name: "dup"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(ctx, CreateOpts{Name: "dup"}); err == nil {
		t.Fatal("expected a name collision error on the second Create")
	}
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
