package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/sandbox"
	"github.com/mq37/poisson/internal/testutil"
)

// denyAll is an ApprovalFn that always denies — used to prove a sandboxed
// call bypasses it entirely rather than merely happening to be approved.
func denyAll(context.Context, string, string, string) (bool, string) { return false, "" }

func TestBashTool_SandboxedExecSkipsApproval(t *testing.T) {
	dir := testutil.TempDir(t)
	mgr := sandbox.NewManager(sandbox.NewFakeDriver())
	sb, err := mgr.Create(context.Background(), sandbox.CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	b := NewBashTool(dir, denyAll) // would reject any host call
	b.SetSandboxManager(mgr)

	res, _ := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command":     "echo from_sandbox",
		"description": "echo",
		"sandboxId":   sb.ID,
	}))
	out := bashOut(t, res)
	if strings.TrimSpace(out.Stdout) != "echo from_sandbox" {
		t.Errorf("stdout = %q, want the fake driver's echo of the command", out.Stdout)
	}
	if out.ExitCode != 0 {
		t.Errorf("exitCode = %d, want 0", out.ExitCode)
	}
}

// TestBashTool_SandboxedRejectsForeignID confirms a sandboxId this
// BashTool's Manager never created (or wasn't Authorize'd for) is rejected
// before any Driver call — raw model input must never be trusted just
// because it's shaped like a real id.
func TestBashTool_SandboxedRejectsForeignID(t *testing.T) {
	dir := testutil.TempDir(t)
	mgr := sandbox.NewManager(sandbox.NewFakeDriver())
	b := NewBashTool(dir, alwaysApprove)
	b.SetSandboxManager(mgr)

	res, _ := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command":     "echo hi",
		"description": "echo",
		"sandboxId":   "fake-999",
	}))
	if res.Error == "" {
		t.Fatal("expected an error for a foreign/unowned sandboxId")
	}
	if !strings.Contains(res.Error, "not found") {
		t.Errorf("error = %q, want it to say the sandbox was not found", res.Error)
	}
}

// TestBashTool_SandboxedNoManagerConfigured covers a registry that never
// had a Manager wired at all (host-only session): a sandboxId still fails
// clearly instead of nil-panicking.
func TestBashTool_SandboxedNoManagerConfigured(t *testing.T) {
	dir := testutil.TempDir(t)
	b := NewBashTool(dir, alwaysApprove) // no SetSandboxManager call

	res, _ := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command":     "echo hi",
		"description": "echo",
		"sandboxId":   "anything",
	}))
	if res.Error == "" {
		t.Fatal("expected an error when no sandbox manager is configured")
	}
	if !strings.Contains(res.Error, "no sandbox manager") {
		t.Errorf("error = %q, want it to say no sandbox manager is available", res.Error)
	}
}

// TestBashTool_SandboxedStillBlocksPoissonYolo confirms the yolo block runs
// before the sandboxId branch, so it can't be smuggled through a sandboxed
// call either.
func TestBashTool_SandboxedStillBlocksPoissonYolo(t *testing.T) {
	dir := testutil.TempDir(t)
	mgr := sandbox.NewManager(sandbox.NewFakeDriver())
	sb, err := mgr.Create(context.Background(), sandbox.CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	b := NewBashTool(dir, alwaysApprove)
	b.SetSandboxManager(mgr)

	res, _ := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command":     "px --yolo -p 'do stuff'",
		"description": "nested yolo",
		"sandboxId":   sb.ID,
	}))
	if res.Error == "" || !strings.Contains(res.Error, "--yolo") {
		t.Fatalf("expected the yolo block to fire even for a sandboxed call, got: %q", res.Error)
	}
}

// TestBashTool_SandboxedExecFailurePropagates confirms a Driver-level error
// (container gone, exec itself failed) surfaces as a tool error, not a
// silent success.
func TestBashTool_SandboxedExecFailurePropagates(t *testing.T) {
	dir := testutil.TempDir(t)
	driver := sandbox.NewFakeDriver()
	mgr := sandbox.NewManager(driver)
	sb, err := mgr.Create(context.Background(), sandbox.CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	driver.MarkDead(sb.ID) // container died after creation, before this call

	b := NewBashTool(dir, alwaysApprove)
	b.SetSandboxManager(mgr)

	res, _ := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command":     "echo hi",
		"description": "echo",
		"sandboxId":   sb.ID,
	}))
	if res.Error == "" {
		t.Fatal("expected an error when the sandbox container is dead")
	}
}

// TestBatch_MixedHostAndSandboxedBash exercises the real path an agent
// hits: setting up in a sandbox while also reading/running something on the
// host in the same batch round.
func TestBatch_MixedHostAndSandboxedBash(t *testing.T) {
	dir := testutil.TempDir(t)
	mgr := sandbox.NewManager(sandbox.NewFakeDriver())
	sb, err := mgr.Create(context.Background(), sandbox.CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	reg := BuildRegistry(BuildOptions{Cwd: dir, ApprovalFn: alwaysApprove, FileApprovalFn: alwaysApprove, SandboxManager: mgr})

	res, _ := reg.Execute(context.Background(), "batch", mustJSON(t, map[string]interface{}{
		"calls": []map[string]interface{}{
			{"tool": "bash", "input": map[string]string{"command": "echo on_host", "description": "host echo"}},
			{"tool": "bash", "input": map[string]interface{}{"command": "echo on_sandbox", "description": "sandbox echo", "sandboxId": sb.ID}},
		},
	}))
	if res.Error != "" {
		t.Fatalf("batch error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "2 ok") {
		t.Fatalf("content: %q", res.Content)
	}
	if !strings.Contains(res.Content, "on_host") || !strings.Contains(res.Content, "on_sandbox") {
		t.Fatalf("expected both host and sandboxed output in the batch result: %q", res.Content)
	}
}
