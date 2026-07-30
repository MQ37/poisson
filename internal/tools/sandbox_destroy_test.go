package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/sandbox"
	"github.com/mq37/poisson/internal/testutil"
)

func TestSandboxDestroyTool_Basic(t *testing.T) {
	dir := testutil.TempDir(t)
	mgr := sandbox.NewManager(sandbox.NewFakeDriver())
	destroy := NewSandboxDestroyTool(mgr)

	// hostPath here is a real, agent-owned directory (e.g. a project the
	// agent explicitly mounted) — the central safety property under test
	// below is that destroy kills only the container and never touches it.
	ws := testutil.TempDir(t)
	if err := os.WriteFile(filepath.Join(ws, "keep.txt"), []byte("do not delete me"), 0o644); err != nil {
		t.Fatal(err)
	}

	create := NewCreateSandboxTool(dir, mgr, alwaysApprove)
	res, _ := create.Execute(context.Background(), mustJSON(t, map[string]interface{}{"hostPath": ws}))
	if res.Error != "" {
		t.Fatalf("create_sandbox: %s", res.Error)
	}
	var created struct {
		SandboxID string `json:"sandboxId"`
	}
	if err := json.Unmarshal([]byte(res.Content), &created); err != nil {
		t.Fatal(err)
	}

	res, _ = destroy.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"sandboxId": created.SandboxID,
	}))
	if res.Error != "" {
		t.Fatalf("sandbox_destroy error: %s", res.Error)
	}
	if mgr.Owns(created.SandboxID) {
		t.Error("Manager should no longer own the sandbox after destroy")
	}
	// SECURITY: hostPath is the agent's own directory, never poisson's to
	// delete — destroy must leave it (and its contents) completely intact.
	got, err := os.ReadFile(filepath.Join(ws, "keep.txt"))
	if err != nil || string(got) != "do not delete me" {
		t.Fatalf("sandbox_destroy touched the agent-supplied hostPath: err=%v content=%q", err, got)
	}
}

func TestSandboxDestroyTool_ForeignID(t *testing.T) {
	mgr := sandbox.NewManager(sandbox.NewFakeDriver())
	tool := NewSandboxDestroyTool(mgr)

	res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]interface{}{"sandboxId": "fake-999"}))
	if res.Error == "" || !strings.Contains(res.Error, "not found") {
		t.Fatalf("error = %q, want a 'not found' message", res.Error)
	}
}

func TestSandboxDestroyTool_DoubleDestroy(t *testing.T) {
	mgr := sandbox.NewManager(sandbox.NewFakeDriver())
	sb, err := mgr.Create(context.Background(), sandbox.CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	tool := NewSandboxDestroyTool(mgr)

	res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]interface{}{"sandboxId": sb.ID}))
	if res.Error != "" {
		t.Fatalf("first destroy: %s", res.Error)
	}
	res, _ = tool.Execute(context.Background(), mustJSON(t, map[string]interface{}{"sandboxId": sb.ID}))
	if res.Error == "" {
		t.Fatal("expected the second destroy to fail cleanly, not succeed silently")
	}
}

func TestSandboxDestroyTool_NoManagerConfigured(t *testing.T) {
	tool := NewSandboxDestroyTool(nil)
	res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]interface{}{"sandboxId": "anything"}))
	if res.Error == "" || !strings.Contains(res.Error, "not available") {
		t.Fatalf("error = %q, want a clear 'not available' message", res.Error)
	}
}
