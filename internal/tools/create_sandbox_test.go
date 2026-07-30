package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/sandbox"
	"github.com/mq37/poisson/internal/testutil"
)

func TestCreateSandboxTool_Basic(t *testing.T) {
	dir := testutil.TempDir(t)
	mgr := sandbox.NewManager(sandbox.NewFakeDriver())
	tool := NewCreateSandboxTool(dir, mgr, denyAll) // no mounts/env → approvalFn must never even be consulted

	res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]interface{}{}))
	if res.Error != "" {
		t.Fatalf("create_sandbox error: %s", res.Error)
	}
	var out struct {
		SandboxID string `json:"sandboxId"`
		HostPath  string `json:"hostPath"`
	}
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatalf("unmarshal: %v (content=%q)", err, res.Content)
	}
	if out.SandboxID == "" || out.HostPath == "" {
		t.Fatalf("expected both sandboxId and hostPath, got %+v", out)
	}
	if !mgr.Owns(out.SandboxID) {
		t.Error("Manager should own the id create_sandbox just created")
	}
	if info, err := os.Stat(out.HostPath); err != nil || !info.IsDir() {
		t.Errorf("hostPath %q should be a real, existing directory", out.HostPath)
	}
}

func TestCreateSandboxTool_DefaultAndCustomImage(t *testing.T) {
	dir := testutil.TempDir(t)
	driver := sandbox.NewFakeDriver()
	mgr := sandbox.NewManager(driver)
	tool := NewCreateSandboxTool(dir, mgr, alwaysApprove)

	if _, err := tool.Execute(context.Background(), mustJSON(t, map[string]interface{}{})); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Execute(context.Background(), mustJSON(t, map[string]interface{}{"image": "custom:tag"})); err != nil {
		t.Fatal(err)
	}
	if len(driver.CreateCalls) != 2 {
		t.Fatalf("CreateCalls = %d, want 2", len(driver.CreateCalls))
	}
	if driver.CreateCalls[0].Image != defaultSandboxImage {
		t.Errorf("default image = %q, want %q", driver.CreateCalls[0].Image, defaultSandboxImage)
	}
	if driver.CreateCalls[1].Image != "custom:tag" {
		t.Errorf("custom image = %q, want custom:tag", driver.CreateCalls[1].Image)
	}
}

func TestCreateSandboxTool_MountsRequireApproval(t *testing.T) {
	dir := testutil.TempDir(t)
	mgr := sandbox.NewManager(sandbox.NewFakeDriver())

	denied := NewCreateSandboxTool(dir, mgr, denyAll)
	res, _ := denied.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"mounts": []map[string]interface{}{{"hostPath": "/home/me/creds", "containerPath": "/creds", "readOnly": true}},
	}))
	if res.Error == "" {
		t.Fatal("expected a mount request to be denied when approvalFn denies")
	}

	var gotCommand string
	spy := func(_ context.Context, command, _, _ string) (bool, string) {
		gotCommand = command
		return true, ""
	}
	approved := NewCreateSandboxTool(dir, mgr, spy)
	res, _ = approved.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"mounts": []map[string]interface{}{{"hostPath": "/home/me/creds", "containerPath": "/creds", "readOnly": true}},
	}))
	if res.Error != "" {
		t.Fatalf("expected approval to let the request through: %s", res.Error)
	}
	if !strings.Contains(gotCommand, "/home/me/creds") || !strings.Contains(gotCommand, "/creds") {
		t.Errorf("approval prompt %q should show the exact host/container paths", gotCommand)
	}
}

func TestCreateSandboxTool_EnvRequiresApprovalAndValuesAreRedacted(t *testing.T) {
	dir := testutil.TempDir(t)
	mgr := sandbox.NewManager(sandbox.NewFakeDriver())

	// Default (nil-equivalent deny) — no approvalFn given at all still
	// denies via BuildOptions-style deny-all semantics if the caller passes
	// denyAll explicitly here.
	denied := NewCreateSandboxTool(dir, mgr, denyAll)
	res, _ := denied.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"env": []string{"SECRET_TOKEN=super-secret-value"},
	}))
	if res.Error == "" {
		t.Fatal("expected an env request to be denied when approvalFn denies")
	}

	var gotCommand string
	spy := func(_ context.Context, command, _, _ string) (bool, string) {
		gotCommand = command
		return true, ""
	}
	approved := NewCreateSandboxTool(dir, mgr, spy)
	res, _ = approved.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"env": []string{"SECRET_TOKEN=super-secret-value"},
	}))
	if res.Error != "" {
		t.Fatalf("expected approval to let the request through: %s", res.Error)
	}
	if !strings.Contains(gotCommand, "SECRET_TOKEN") {
		t.Errorf("approval prompt %q should show the env key", gotCommand)
	}
	if strings.Contains(gotCommand, "super-secret-value") {
		t.Errorf("approval prompt %q must not echo the env value", gotCommand)
	}
}

func TestCreateSandboxTool_NoManagerConfigured(t *testing.T) {
	dir := testutil.TempDir(t)
	tool := NewCreateSandboxTool(dir, nil, alwaysApprove)
	res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]interface{}{}))
	if res.Error == "" || !strings.Contains(res.Error, "not available") {
		t.Fatalf("error = %q, want a clear 'not available' message", res.Error)
	}
}

func TestCreateSandboxTool_CleansUpWorkspaceOnDriverFailure(t *testing.T) {
	dir := testutil.TempDir(t)
	driver := sandbox.NewFakeDriver()
	driver.CreateErr = errors.New("boom: driver refused to create")
	mgr := sandbox.NewManager(driver)
	tool := NewCreateSandboxTool(dir, mgr, alwaysApprove)

	before, _ := filepath.Glob(filepath.Join(os.TempDir(), "poisson-sandbox-*"))
	res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]interface{}{}))
	if res.Error == "" {
		t.Fatal("expected an error when the driver fails to create the container")
	}
	after, _ := filepath.Glob(filepath.Join(os.TempDir(), "poisson-sandbox-*"))
	if len(after) > len(before) {
		t.Errorf("scratch workspace leaked in os.TempDir(): before=%v, after=%v", before, after)
	}
}

// TestNewScratchWorkspace_CleanupRemovesTree is the direct unit test for the
// helper's cleanup contract, independent of any tool wiring.
func TestNewScratchWorkspace_CleanupRemovesTree(t *testing.T) {
	hostPath, cleanup, err := newScratchWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(hostPath); err != nil || !info.IsDir() {
		t.Fatalf("workspace dir should exist: %v", err)
	}
	cleanup()
	if _, err := os.Stat(hostPath); !os.IsNotExist(err) {
		t.Errorf("workspace tree should be gone after cleanup, stat err = %v", err)
	}
}
