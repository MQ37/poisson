package sandbox

// Gated integration suite against the REAL podman CLI — skipped by default
// (opt in with PODMAN_INTEGRATION=1) since it needs a real podman install
// and actually creates/destroys real containers. Everything else in this
// package tests against FakeDriver and needs no podman at all.
//
// Confined to /tmp for the entire run (see docs/sandbox-plan.md's
// "Disk-wear guard" section) — --root/--runroot plus the XDG_*/TMPDIR
// overrides that close the gaps those two flags alone don't cover,
// confirmed empirically (before/after snapshot of the real
// ~/.local/share/containers, ~/.config/containers,
// /run/user/$UID/{containers,libpod} — identical, zero touch) both in an
// earlier manual session and by TestPodmanIntegration_ConfinedToTmp below.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// integrationImage is a pinned tag, matching internal/tools/create_sandbox.go's
// defaultSandboxImage (duplicated, not imported: internal/tools already
// imports internal/sandbox, so the reverse import would cycle).
const integrationImage = "ubuntu:26.04"

func requirePodmanIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("PODMAN_INTEGRATION") != "1" {
		t.Skip("set PODMAN_INTEGRATION=1 to run real-podman integration tests")
	}
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skip("podman not installed")
	}
}

// tmpIsTmpfs reports whether /tmp is tmpfs on this host — a harness running
// somewhere else must not silently confine podman's storage to a
// disk-backed /tmp and call it disk-wear-safe.
func tmpIsTmpfs(t *testing.T) bool {
	t.Helper()
	out, err := exec.Command("findmnt", "-n", "-o", "FSTYPE", "/tmp").Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "tmpfs"
}

// confinedDriver builds a podmanDriver whose storage is fully confined to a
// fresh directory under /tmp, per docs/sandbox-plan.md's disk-wear guard.
// Registers cleanup: podman rm -f -t 0 is the caller's job for any
// container it created (kept per-test, not here, since Kill is itself
// under test); this only tears down the confined storage root itself, via
// `podman system reset --force` (the reliable teardown, confirmed
// empirically — a raw `rm -rf`/`podman unshare rm -rf` right after
// container removal races the kernel's own async overlay/netns unmount and
// intermittently fails as "Device or resource busy"; `system reset --force`
// waits out that teardown properly before this test ever touches the
// directory tree directly).
func confinedDriver(t *testing.T) *podmanDriver {
	t.Helper()
	requirePodmanIntegration(t)
	if !tmpIsTmpfs(t) {
		t.Skip("/tmp is not tmpfs on this host — refusing to confine podman storage there (see docs/sandbox-plan.md's disk-wear guard)")
	}

	base, err := os.MkdirTemp("", "poisson-podman-test.*")
	if err != nil {
		t.Fatalf("mkdir confined base: %v", err)
	}
	dirs := map[string]string{
		"data": "", "config": "", "runtime": "", "tmp": "", "root": "", "run": "",
	}
	for name := range dirs {
		if err := os.MkdirAll(filepath.Join(base, name), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
	}
	if err := os.Chmod(filepath.Join(base, "runtime"), 0o700); err != nil {
		t.Fatalf("chmod runtime: %v", err)
	}

	globalArgs := []string{"--root", filepath.Join(base, "root"), "--runroot", filepath.Join(base, "run")}
	extraEnv := []string{
		"XDG_DATA_HOME=" + filepath.Join(base, "data"),
		"XDG_CONFIG_HOME=" + filepath.Join(base, "config"),
		"XDG_RUNTIME_DIR=" + filepath.Join(base, "runtime"),
		"TMPDIR=" + filepath.Join(base, "tmp"),
	}
	d := &podmanDriver{globalArgs: globalArgs, extraEnv: extraEnv, execUser: make(map[string]string)}

	// Fail loudly if this host's rootless podman falls back to the vfs
	// storage driver (full-copy, not copy-on-write) instead of overlay —
	// heavier and slower, defeating the point, and a silent-degrade here
	// would make every other timing/disk-wear claim in this suite meaningless.
	driverName, _, err := d.run(context.Background(), "info", "--format", "{{.Store.GraphDriverName}}")
	if err != nil {
		t.Fatalf("podman info: %v", err)
	}
	if got := strings.TrimSpace(driverName); got != "overlay" {
		t.Fatalf("storage driver = %q, want overlay (vfs is heavier and defeats the disk-wear guard)", got)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		d.run(ctx, "system", "reset", "--force")
		os.RemoveAll(base) // safe now: system reset already tore down the mounts
	})
	return d
}

// snapshotRealPodmanDirs fingerprints the real (non-confined) podman state
// directories this test must never touch — used before/after to prove the
// confinement mechanism actually confines.
func snapshotRealPodmanDirs(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	paths := []string{
		filepath.Join(home, ".local", "share", "containers"),
		filepath.Join(home, ".config", "containers"),
	}
	if rd := os.Getenv("XDG_RUNTIME_DIR"); rd != "" {
		paths = append(paths, filepath.Join(rd, "containers"), filepath.Join(rd, "libpod"))
	}
	for _, root := range paths {
		filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil {
				return nil
			}
			b.WriteString(path)
			if !info.IsDir() {
				fmt.Fprintf(&b, ":%s:%d", info.ModTime(), info.Size())
			}
			b.WriteString("\n")
			return nil
		})
	}
	return b.String()
}

// TestPodmanIntegration_ConfinedToTmp is the automated regression version
// of the manual before/after snapshot verification from earlier in this
// project's history: a real create+start+bootstrap+exec+kill cycle against
// a confinedDriver must leave zero trace in the real (non-confined) podman
// state directories.
func TestPodmanIntegration_ConfinedToTmp(t *testing.T) {
	d := confinedDriver(t)
	before := snapshotRealPodmanDirs(t)

	ctx := context.Background()
	ws := t.TempDir()
	id, err := d.Create(ctx, CreateOpts{Image: integrationImage, HostPath: ws})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, _, _, err := d.Exec(ctx, id, "echo hi", "", 30*time.Second); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if err := d.Kill(ctx, id); err != nil {
		t.Fatalf("Kill: %v", err)
	}

	after := snapshotRealPodmanDirs(t)
	if before != after {
		t.Fatalf("confined podman touched real state dirs\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestPodmanIntegration_FullLifecycle exercises Create's real bootstrap
// (matching-uid user reuse — ubuntu:26.04 ships a pre-existing uid-1000
// "ubuntu" user, confirmed empirically — plus passwordless sudo), Exec
// running as that resolved user with real workspace read/write, sudo
// actually working, Inspect, and Kill — the whole real Driver contract,
// not just the mechanical shape FakeDriver-backed tests already cover.
func TestPodmanIntegration_FullLifecycle(t *testing.T) {
	d := confinedDriver(t)
	ctx := context.Background()
	ws := t.TempDir()

	id, err := d.Create(ctx, CreateOpts{Image: integrationImage, HostPath: ws})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	alive, err := d.Inspect(ctx, id)
	if err != nil || !alive {
		t.Fatalf("Inspect after Create = %v, %v, want true, nil", alive, err)
	}

	// Ordinary exec: not root (sudo is the only path to root, per the
	// design), defaults into /workspace, can write there.
	stdout, _, exitCode, err := d.Exec(ctx, id, "whoami && pwd && echo hello > f.txt && cat f.txt", "", 30*time.Second)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("exitCode = %d, want 0, stdout=%q", exitCode, stdout)
	}
	if strings.Contains(stdout, "root") {
		t.Errorf("ordinary exec ran as root, want the resolved non-root user: %q", stdout)
	}
	if !strings.Contains(stdout, "/workspace") {
		t.Errorf("expected the default workdir to be /workspace: %q", stdout)
	}
	if got, err := os.ReadFile(filepath.Join(ws, "f.txt")); err != nil || strings.TrimSpace(string(got)) != "hello" {
		t.Fatalf("workspace file on host = %q, %v, want \"hello\"", got, err)
	}

	// sudo works, passwordless.
	_, stderr, exitCode, err := d.Exec(ctx, id, "sudo whoami", "", 30*time.Second)
	if err != nil {
		t.Fatalf("sudo Exec: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("sudo whoami exitCode = %d, stderr=%q", exitCode, stderr)
	}

	if err := d.Kill(ctx, id); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	alive, err = d.Inspect(ctx, id)
	if err != nil {
		t.Fatalf("Inspect after Kill: %v", err)
	}
	if alive {
		t.Fatal("Inspect after Kill = true, want false")
	}
}
