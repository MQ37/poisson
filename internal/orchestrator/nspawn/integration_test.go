package nspawn

// Gated integration suite against a REAL systemd-nspawn/machinectl/
// systemd-run stack — skipped by default (opt in with NSPAWN_INTEGRATION=1)
// since it needs root, a real golden image already pulled (see
// docs/orchestrator-plan.md M0 Step 3), and actually creates/destroys a
// real instance on whatever host runs it (orchestrator-host in production). Follows
// the same gating idiom as internal/sandbox/podman_integration_test.go.
//
// Everything else in this package (argv_test.go) tests pure argv/content
// builders and needs no real binaries at all.

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/orchestrator"
)

func requireNspawnIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("NSPAWN_INTEGRATION") != "1" {
		t.Skip("set NSPAWN_INTEGRATION=1 to run real-nspawn integration tests (root, a pulled golden image, orchestrator-host or equivalent)")
	}
	if os.Geteuid() != 0 {
		t.Skip("nspawn integration suite needs root")
	}
	for _, bin := range []string{"systemd-nspawn", "machinectl", "systemctl", "systemd-run"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not found", bin)
		}
	}
}

// TestNspawnIntegration_FullLifecycle exercises Create -> Start -> Exec ->
// Destroy against the real golden image, checking every Step 12 verify
// criterion: the instance appears in machinectl list, runs as real uid 0,
// has CAP_SYS_MODULE absent but CAP_SYS_ADMIN present, and its unit reports
// the exact configured MemoryMax in bytes.
func TestNspawnIntegration_FullLifecycle(t *testing.T) {
	requireNspawnIntegration(t)

	cfg := DefaultConfig()
	rt := NewRuntime(cfg)
	ctx := context.Background()

	name, err := orchestrator.ResolveInstanceName("integration-test")
	if err != nil {
		t.Fatalf("ResolveInstanceName: %v", err)
	}
	spec := orchestrator.InstanceSpec{Name: name, Provider: "anthropic", Model: "claude-sonnet-5"}

	t.Cleanup(func() {
		dctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := rt.Destroy(dctx, name); err != nil {
			t.Logf("cleanup Destroy(%s): %v", name, err)
		}
	})

	if err := rt.Create(ctx, spec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	startCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := rt.Start(startCtx, name); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Appears in machinectl list now that it's actually running.
	out, _, err := rt.run(ctx, machinectlBin, "list", "--no-legend")
	if err != nil {
		t.Fatalf("machinectl list: %v", err)
	}
	if !strings.Contains(out, name) {
		t.Errorf("machinectl list = %q, want it to contain running instance %s", out, name)
	}

	// Real uid 0 inside.
	stdout, stderr, code, err := rt.Exec(ctx, name, []string{"/usr/bin/id", "-u"})
	if err != nil {
		t.Fatalf("Exec id -u: %v (stderr: %s)", err, stderr)
	}
	if code != 0 {
		t.Fatalf("Exec id -u exit code = %d, stderr = %s", code, stderr)
	}
	if uid := strings.TrimSpace(stdout); uid != "0" {
		t.Errorf("uid inside instance = %q, want 0 (real root)", uid)
	}

	// CAP_SYS_MODULE absent, CAP_SYS_ADMIN present.
	stdout, stderr, code, err = rt.Exec(ctx, name, []string{"capsh", "--print"})
	if err != nil {
		t.Fatalf("Exec capsh: %v (stderr: %s)", err, stderr)
	}
	if code != 0 {
		t.Fatalf("Exec capsh exit code = %d, stderr = %s", code, stderr)
	}
	// capsh's "Current: ...cap_sys_module-ep..." line names every capability
	// class it discusses, dropped or not — a bare substring match on
	// "cap_sys_module" anywhere in the output would also match that line.
	// The actual bounding-set membership (what --drop-capability= controls)
	// is the "Bounding set =" line specifically.
	boundingSet := boundingSetLine(stdout)
	if boundingSet == "" {
		t.Fatalf("capsh --print output missing a \"Bounding set =\" line:\n%s", stdout)
	}
	if strings.Contains(boundingSet, "cap_sys_module") {
		t.Errorf("bounding set contains cap_sys_module, want it dropped: %s", boundingSet)
	}
	if !strings.Contains(boundingSet, "cap_sys_admin") {
		t.Errorf("bounding set missing cap_sys_admin, want genuine root minus only CAP_SYS_MODULE: %s", boundingSet)
	}

	// MemoryMax reports exactly 1200*1024*1024 bytes.
	memOut, _, err := rt.run(ctx, systemctlBin, "show", unitName(name), "-p", "MemoryMax", "--value")
	if err != nil {
		t.Fatalf("systemctl show MemoryMax: %v", err)
	}
	memVal, err := strconv.ParseInt(strings.TrimSpace(memOut), 10, 64)
	if err != nil {
		t.Fatalf("parse MemoryMax %q: %v", memOut, err)
	}
	if want := int64(1200 * 1024 * 1024); memVal != want {
		t.Errorf("MemoryMax = %d, want %d", memVal, want)
	}
}

// boundingSetLine returns capsh --print's "Bounding set = ..." line (empty
// if not found).
func boundingSetLine(capshOutput string) string {
	for _, line := range strings.Split(capshOutput, "\n") {
		if strings.Contains(line, "Bounding set") {
			return line
		}
	}
	return ""
}

// TestNspawnIntegration_StartTurn runs one real agent turn inside a real
// instance end to end: Create (authorized for the host's default
// provider) -> Start -> StartTurn -> drain ReadEvent until "done". Proves
// the whole StartTurn/AttachChild/turnRunArgs chain against a real
// systemd-run --pipe process and a real (bind-mounted, scoped) credential,
// not just the argv shape argv_test.go already covers.
func TestNspawnIntegration_StartTurn(t *testing.T) {
	requireNspawnIntegration(t)
	provider := os.Getenv("NSPAWN_INTEGRATION_PROVIDER")
	model := os.Getenv("NSPAWN_INTEGRATION_MODEL")
	if provider == "" || model == "" {
		t.Skip("set NSPAWN_INTEGRATION_PROVIDER/_MODEL to a real configured provider/model to run a live turn")
	}

	cfg := DefaultConfig()
	rt := NewRuntime(cfg)
	ctx := context.Background()

	name, err := orchestrator.ResolveInstanceName("integration-turn")
	if err != nil {
		t.Fatal(err)
	}
	spec := orchestrator.InstanceSpec{Name: name, Provider: provider, Model: model, AuthorizedProviders: []string{provider}}

	t.Cleanup(func() {
		dctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := rt.Destroy(dctx, name); err != nil {
			t.Logf("cleanup Destroy(%s): %v", name, err)
		}
	})

	if err := rt.Create(ctx, spec); err != nil {
		t.Fatalf("Create: %v", err)
	}
	startCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := rt.Start(startCtx, name); err != nil {
		t.Fatalf("Start: %v", err)
	}

	turnSpec := orchestrator.TurnSpec{
		SessionID: "s-integration-turn", Provider: provider, Model: model,
		Message: "say literally just the word hi, nothing else", Yolo: true,
	}
	turn, err := rt.StartTurn(ctx, name, turnSpec)
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}

	var sawDone bool
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		ev, err := turn.ReadEvent()
		if err != nil {
			break // EOF once the turn process exits
		}
		if ev == nil {
			continue
		}
		t.Logf("event: type=%s text=%q error=%q", ev.Type, ev.Text, ev.Error)
		if ev.Type == "done" {
			sawDone = true
			if !ev.Success {
				t.Errorf("turn done event success=false, error=%q", ev.Error)
			}
			break
		}
	}
	if !sawDone {
		t.Error("never saw a \"done\" event before EOF/deadline")
	}
}

// TestNspawnIntegration_DestroyIsIdempotent checks a second Destroy on an
// already-gone instance is success, not an error.
func TestNspawnIntegration_DestroyIsIdempotent(t *testing.T) {
	requireNspawnIntegration(t)
	rt := NewRuntime(DefaultConfig())
	ctx := context.Background()

	name, err := orchestrator.ResolveInstanceName("integration-idempotent")
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Destroy(ctx, name); err != nil {
		t.Errorf("Destroy on a never-created instance = %v, want nil (idempotent)", err)
	}
}
