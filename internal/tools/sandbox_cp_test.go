package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/sandbox"
	"github.com/mq37/poisson/internal/testutil"
)

// newTestSandbox creates a real Manager-tracked sandbox whose HostPath is a
// real, empty directory on disk (via a temp dir, not the real
// newScratchWorkspace/os.TempDir path — cheaper to clean up under the
// test's own t.TempDir()).
func newTestSandbox(t *testing.T, mgr *sandbox.Manager) sandbox.Sandbox {
	t.Helper()
	ws := filepath.Join(testutil.TempDir(t), "workspace")
	if err := os.Mkdir(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	sb, err := mgr.Create(context.Background(), sandbox.CreateOpts{HostPath: ws})
	if err != nil {
		t.Fatal(err)
	}
	return sb
}

func TestSandboxCpTool_CopyIn(t *testing.T) {
	dir := testutil.TempDir(t)
	mgr := sandbox.NewManager(sandbox.NewFakeDriver())
	sb := newTestSandbox(t, mgr)

	src := filepath.Join(dir, "source.txt")
	if err := os.WriteFile(src, []byte("hello from host"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewSandboxCpTool(dir, mgr, alwaysApprove)
	res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"sandboxId": sb.ID, "direction": "in", "hostPath": src, "workspacePath": "dest.txt",
	}))
	if res.Error != "" {
		t.Fatalf("sandbox_cp in error: %s", res.Error)
	}
	got, err := os.ReadFile(filepath.Join(sb.HostPath, "dest.txt"))
	if err != nil {
		t.Fatalf("expected file copied into workspace: %v", err)
	}
	if string(got) != "hello from host" {
		t.Errorf("content = %q, want the source file's content", got)
	}
}

func TestSandboxCpTool_CopyOut(t *testing.T) {
	dir := testutil.TempDir(t)
	mgr := sandbox.NewManager(sandbox.NewFakeDriver())
	sb := newTestSandbox(t, mgr)

	if err := os.WriteFile(filepath.Join(sb.HostPath, "artifact.bin"), []byte("build output"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "extracted.bin")

	tool := NewSandboxCpTool(dir, mgr, alwaysApprove)
	res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"sandboxId": sb.ID, "direction": "out", "hostPath": dst, "workspacePath": "artifact.bin",
	}))
	if res.Error != "" {
		t.Fatalf("sandbox_cp out error: %s", res.Error)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("expected file copied to host: %v", err)
	}
	if string(got) != "build output" {
		t.Errorf("content = %q, want the workspace file's content", got)
	}
}

// TestSandboxCpTool_AbsoluteWorkspacePathRebasesNotEscapes is the central
// security property: a model passing an absolute-looking workspacePath like
// "/etc/passwd" must land inside the sandbox's own workspace, never touch
// the real host /etc/passwd.
func TestSandboxCpTool_AbsoluteWorkspacePathRebasesNotEscapes(t *testing.T) {
	dir := testutil.TempDir(t)
	mgr := sandbox.NewManager(sandbox.NewFakeDriver())
	sb := newTestSandbox(t, mgr)

	src := filepath.Join(dir, "payload.txt")
	if err := os.WriteFile(src, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewSandboxCpTool(dir, mgr, alwaysApprove)
	res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"sandboxId": sb.ID, "direction": "in", "hostPath": src, "workspacePath": "/etc/passwd",
	}))
	if res.Error != "" {
		t.Fatalf("expected the absolute path to be rebased, not rejected: %s", res.Error)
	}
	if _, err := os.Stat("/etc/passwd"); err == nil {
		// /etc/passwd exists on this host (it does on any Linux box) —
		// confirm it was NOT touched: its content must not be "payload".
		got, _ := os.ReadFile("/etc/passwd")
		if string(got) == "payload" {
			t.Fatal("SECURITY: sandbox_cp wrote to the real host /etc/passwd")
		}
	}
	landed, err := os.ReadFile(filepath.Join(sb.HostPath, "etc", "passwd"))
	if err != nil || string(landed) != "payload" {
		t.Fatalf("expected the payload to land inside the workspace at etc/passwd, err=%v content=%q", err, landed)
	}
}

// TestSandboxCpTool_SymlinkEscapeRejected plants a symlink inside the
// workspace pointing outside it, then tries to copy through that name —
// the exact escape the plan doc's symlink-safety section is about.
func TestSandboxCpTool_SymlinkEscapeRejected(t *testing.T) {
	dir := testutil.TempDir(t)
	mgr := sandbox.NewManager(sandbox.NewFakeDriver())
	sb := newTestSandbox(t, mgr)

	outsideTarget := filepath.Join(dir, "outside-secret.txt")
	if err := os.WriteFile(outsideTarget, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(sb.HostPath, "escape-link")
	if err := os.Symlink(outsideTarget, link); err != nil {
		t.Fatal(err)
	}

	src := filepath.Join(dir, "attacker.txt")
	if err := os.WriteFile(src, []byte("attacker-controlled"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewSandboxCpTool(dir, mgr, alwaysApprove)
	res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"sandboxId": sb.ID, "direction": "in", "hostPath": src, "workspacePath": "escape-link",
	}))
	if res.Error == "" {
		t.Fatal("expected the symlink escape to be rejected")
	}
	got, err := os.ReadFile(outsideTarget)
	if err != nil || string(got) != "original" {
		t.Fatalf("SECURITY: escape-link let sandbox_cp write outside the workspace: err=%v content=%q", err, got)
	}
}

func TestSandboxCpTool_HostPathSensitiveRequiresApproval(t *testing.T) {
	dir := testutil.TempDir(t)
	mgr := sandbox.NewManager(sandbox.NewFakeDriver())
	sb := newTestSandbox(t, mgr)

	sensitive := filepath.Join(dir, ".env")
	if err := os.WriteFile(sensitive, []byte("SECRET=1"), 0o644); err != nil {
		t.Fatal(err)
	}

	denied := NewSandboxCpTool(dir, mgr, denyAll)
	res, _ := denied.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"sandboxId": sb.ID, "direction": "in", "hostPath": sensitive, "workspacePath": "env.txt",
	}))
	if res.Error == "" {
		t.Fatal("expected a sensitive hostPath to require approval")
	}

	approved := NewSandboxCpTool(dir, mgr, alwaysApprove)
	res, _ = approved.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"sandboxId": sb.ID, "direction": "in", "hostPath": sensitive, "workspacePath": "env.txt",
	}))
	if res.Error != "" {
		t.Fatalf("expected approval to let a sensitive hostPath through: %s", res.Error)
	}
}

func TestSandboxCpTool_ForeignSandboxID(t *testing.T) {
	dir := testutil.TempDir(t)
	mgr := sandbox.NewManager(sandbox.NewFakeDriver())
	tool := NewSandboxCpTool(dir, mgr, alwaysApprove)

	res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"sandboxId": "fake-999", "direction": "in", "hostPath": "/tmp/whatever", "workspacePath": "x",
	}))
	if res.Error == "" || !strings.Contains(res.Error, "not found") {
		t.Fatalf("error = %q, want a 'not found' message for a foreign sandboxId", res.Error)
	}
}

func TestSandboxCpTool_InvalidDirection(t *testing.T) {
	dir := testutil.TempDir(t)
	mgr := sandbox.NewManager(sandbox.NewFakeDriver())
	sb := newTestSandbox(t, mgr)
	tool := NewSandboxCpTool(dir, mgr, alwaysApprove)

	res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"sandboxId": sb.ID, "direction": "sideways", "hostPath": "/tmp/x", "workspacePath": "x",
	}))
	if res.Error == "" {
		t.Fatal("expected an invalid direction to be rejected")
	}
}

func TestSandboxCpTool_NoManagerConfigured(t *testing.T) {
	dir := testutil.TempDir(t)
	tool := NewSandboxCpTool(dir, nil, alwaysApprove)
	res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"sandboxId": "anything", "direction": "in", "hostPath": "/tmp/x", "workspacePath": "x",
	}))
	if res.Error == "" || !strings.Contains(res.Error, "not available") {
		t.Fatalf("error = %q, want a clear 'not available' message", res.Error)
	}
}
