package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/sandbox"
	"github.com/mq37/poisson/internal/testutil"
)

// TestSandboxTool_ActionRequired confirms a call with no action gets a
// clear error, not a silent default to one action.
func TestSandboxTool_ActionRequired(t *testing.T) {
	dir := testutil.TempDir(t)
	mgr := sandbox.NewManager(sandbox.NewFakeDriver())
	tool := NewSandboxTool(dir, mgr, alwaysApprove, alwaysApprove, true)

	res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]interface{}{}))
	if res.Error == "" || !strings.Contains(res.Error, "action is required") {
		t.Fatalf("error = %q, want an 'action is required' message", res.Error)
	}
}

// TestSandboxTool_UnknownActionRejected confirms a bogus action name is
// rejected with a clear, actionable error.
func TestSandboxTool_UnknownActionRejected(t *testing.T) {
	dir := testutil.TempDir(t)
	mgr := sandbox.NewManager(sandbox.NewFakeDriver())
	tool := NewSandboxTool(dir, mgr, alwaysApprove, alwaysApprove, true)

	res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]interface{}{"action": "delete"}))
	if res.Error == "" || !strings.Contains(res.Error, "unknown action") {
		t.Fatalf("error = %q, want an 'unknown action' message", res.Error)
	}
}

// TestSandboxTool_DispatchesEachActionToTheRightUnderlyingTool exercises the
// full create -> list -> resurrect -> destroy lifecycle through one
// SandboxTool instance, proving each action reaches its own underlying
// tool's real, independently-tested logic rather than a reimplementation.
func TestSandboxTool_DispatchesEachActionToTheRightUnderlyingTool(t *testing.T) {
	dir := testutil.TempDir(t)
	driver := sandbox.NewFakeDriver()
	mgr := sandbox.NewManager(driver)
	tool := NewSandboxTool(dir, mgr, alwaysApprove, alwaysApprove, true)
	ctx := context.Background()

	// create
	res, _ := tool.Execute(ctx, mustJSON(t, map[string]interface{}{"action": "create", "name": "lifecycle-test"}))
	if res.Error != "" {
		t.Fatalf("create: %s", res.Error)
	}
	var created struct {
		SandboxID string `json:"sandboxId"`
	}
	if err := json.Unmarshal([]byte(res.Content), &created); err != nil {
		t.Fatal(err)
	}
	if created.SandboxID != "px-sandbox-lifecycle-test" {
		t.Fatalf("sandboxId = %q, want px-sandbox-lifecycle-test", created.SandboxID)
	}

	// list
	res, _ = tool.Execute(ctx, mustJSON(t, map[string]interface{}{"action": "list"}))
	if res.Error != "" || !strings.Contains(res.Content, created.SandboxID) {
		t.Fatalf("list did not show the created sandbox: err=%q content=%q", res.Error, res.Content)
	}

	// resurrect (no-op on an already-running sandbox, still must succeed)
	res, _ = tool.Execute(ctx, mustJSON(t, map[string]interface{}{"action": "resurrect", "sandboxId": created.SandboxID}))
	if res.Error != "" {
		t.Fatalf("resurrect: %s", res.Error)
	}

	// destroy
	res, _ = tool.Execute(ctx, mustJSON(t, map[string]interface{}{"action": "destroy", "sandboxId": created.SandboxID}))
	if res.Error != "" {
		t.Fatalf("destroy: %s", res.Error)
	}
	if mgr.Owns(created.SandboxID) {
		t.Error("Manager should no longer own the sandbox after action=destroy")
	}
}

// TestSandboxTool_ChildRejectsCreateAction confirms allowCreate=false (a
// subagent's registry) both excludes "create" from the schema's action enum
// and rejects it defensively at Execute time, in case a model sends it
// anyway despite the schema.
func TestSandboxTool_ChildRejectsCreateAction(t *testing.T) {
	dir := testutil.TempDir(t)
	mgr := sandbox.NewManager(sandbox.NewFakeDriver())
	tool := NewSandboxTool(dir, mgr, alwaysApprove, alwaysApprove, false)

	var schema struct {
		Properties struct {
			Action struct {
				Enum []string `json:"enum"`
			} `json:"action"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(tool.Schema(), &schema); err != nil {
		t.Fatal(err)
	}
	for _, a := range schema.Properties.Action.Enum {
		if a == "create" {
			t.Error("child sandbox tool's schema must not offer action=create")
		}
	}

	res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]interface{}{"action": "create"}))
	if res.Error == "" {
		t.Fatal("expected action=create to be rejected when allowCreate is false")
	}
}

// TestSandboxTool_NoManagerConfigured confirms every action fails cleanly,
// not with a nil-pointer panic, when no Manager is wired at all.
func TestSandboxTool_NoManagerConfigured(t *testing.T) {
	dir := testutil.TempDir(t)
	tool := NewSandboxTool(dir, nil, alwaysApprove, alwaysApprove, true)

	for _, action := range []string{"create", "cp", "destroy", "resurrect", "list"} {
		res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]interface{}{
			"action": action, "sandboxId": "x", "direction": "in", "hostPath": "/tmp/a", "workspacePath": "b",
		}))
		if res.Error == "" || !strings.Contains(res.Error, "not available") {
			t.Errorf("action=%s: error = %q, want a clear 'not available' message", action, res.Error)
		}
	}
}
