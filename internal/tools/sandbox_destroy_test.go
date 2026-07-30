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

	// Go through the real create_sandbox scratch-workspace layout so this
	// test also proves the whole <base> tree (not just hostPath itself)
	// gets removed, matching newScratchWorkspace's <base>/workspace shape.
	create := NewCreateSandboxTool(dir, mgr, alwaysApprove)
	res, _ := create.Execute(context.Background(), mustJSON(t, map[string]interface{}{}))
	if res.Error != "" {
		t.Fatalf("create_sandbox: %s", res.Error)
	}
	var created struct {
		SandboxID string `json:"sandboxId"`
		HostPath  string `json:"hostPath"`
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
	base := filepath.Dir(created.HostPath)
	if _, err := os.Stat(base); !os.IsNotExist(err) {
		t.Errorf("expected the whole scratch base %q to be removed, stat err = %v", base, err)
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
