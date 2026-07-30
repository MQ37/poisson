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

// TestCreateSandboxTool_Basic: a plain call with no hostPath/mounts/env gets
// an isolated container with no host-backed workspace at all — there is no
// default /tmp scratch dir anymore. approvalFn must never even be
// consulted, since nothing here touches the host filesystem.
func TestCreateSandboxTool_Basic(t *testing.T) {
	dir := testutil.TempDir(t)
	mgr := sandbox.NewManager(sandbox.NewFakeDriver())
	tool := NewCreateSandboxTool(dir, mgr, denyAll)

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
	if out.SandboxID == "" {
		t.Fatalf("expected a sandboxId, got %+v", out)
	}
	if out.HostPath != "" {
		t.Errorf("expected no hostPath without an explicit one, got %q", out.HostPath)
	}
	if !mgr.Owns(out.SandboxID) {
		t.Error("Manager should own the id create_sandbox just created")
	}
}

// TestCreateSandboxTool_HostPathRequiresApprovalAndIsUsedAsIs: hostPath is
// agent-supplied, gated the same as mounts, and passed through verbatim —
// never copied into a poisson-managed location.
func TestCreateSandboxTool_HostPathRequiresApprovalAndIsUsedAsIs(t *testing.T) {
	dir := testutil.TempDir(t)
	ws := testutil.TempDir(t)
	mgr := sandbox.NewManager(sandbox.NewFakeDriver())

	denied := NewCreateSandboxTool(dir, mgr, denyAll)
	res, _ := denied.Execute(context.Background(), mustJSON(t, map[string]interface{}{"hostPath": ws}))
	if res.Error == "" {
		t.Fatal("expected a hostPath request to be denied when approvalFn denies")
	}

	var gotCommand string
	spy := func(_ context.Context, command, _, _ string) (bool, string) {
		gotCommand = command
		return true, ""
	}
	approved := NewCreateSandboxTool(dir, mgr, spy)
	res, _ = approved.Execute(context.Background(), mustJSON(t, map[string]interface{}{"hostPath": ws}))
	if res.Error != "" {
		t.Fatalf("expected approval to let the request through: %s", res.Error)
	}
	if !strings.Contains(gotCommand, ws) {
		t.Errorf("approval prompt %q should show the exact hostPath", gotCommand)
	}
	var out struct {
		HostPath string `json:"hostPath"`
	}
	json.Unmarshal([]byte(res.Content), &out)
	if out.HostPath != ws {
		t.Errorf("hostPath = %q, want the exact path given (%q), never rewritten", out.HostPath, ws)
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

// TestCreateSandboxTool_NamePrefixedAndReturnedAsID confirms an agent-
// supplied name becomes the sandboxId (px-sandbox-<name>), not just a hint
// — that's the whole basis of naming a sandbox to find it again later (see
// docs/sandbox-plan.md's "Crash recovery" section).
func TestCreateSandboxTool_NamePrefixedAndReturnedAsID(t *testing.T) {
	dir := testutil.TempDir(t)
	mgr := sandbox.NewManager(sandbox.NewFakeDriver())
	tool := NewCreateSandboxTool(dir, mgr, denyAll)

	res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]interface{}{"name": "api-testing-2"}))
	if res.Error != "" {
		t.Fatalf("create_sandbox error: %s", res.Error)
	}
	var out struct {
		SandboxID string `json:"sandboxId"`
	}
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatal(err)
	}
	if out.SandboxID != "px-sandbox-api-testing-2" {
		t.Errorf("sandboxId = %q, want px-sandbox-api-testing-2", out.SandboxID)
	}
}

// TestCreateSandboxTool_NameCollisionErrorsClearly confirms reusing a live
// name surfaces a clear error the agent can act on (try another name, or
// check list_sandboxes) instead of silently reusing/corrupting the first.
func TestCreateSandboxTool_NameCollisionErrorsClearly(t *testing.T) {
	dir := testutil.TempDir(t)
	mgr := sandbox.NewManager(sandbox.NewFakeDriver())
	tool := NewCreateSandboxTool(dir, mgr, denyAll)

	if res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]interface{}{"name": "dup"})); res.Error != "" {
		t.Fatalf("first create: %s", res.Error)
	}
	res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]interface{}{"name": "dup"}))
	if res.Error == "" {
		t.Fatal("expected a clear error on a duplicate sandbox name")
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

// TestCreateSandboxTool_NeverTouchesOsTempDir is the regression this whole
// change is about: create_sandbox must never provision anything under
// os.TempDir() on its own — a hostPath is only ever what the agent supplies.
func TestCreateSandboxTool_NeverTouchesOsTempDir(t *testing.T) {
	dir := testutil.TempDir(t)
	mgr := sandbox.NewManager(sandbox.NewFakeDriver())
	tool := NewCreateSandboxTool(dir, mgr, alwaysApprove)

	before, _ := filepath.Glob(filepath.Join(os.TempDir(), "poisson-sandbox-*"))
	if res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]interface{}{})); res.Error != "" {
		t.Fatalf("create_sandbox: %s", res.Error)
	}
	after, _ := filepath.Glob(filepath.Join(os.TempDir(), "poisson-sandbox-*"))
	if len(after) > len(before) {
		t.Errorf("create_sandbox left something under os.TempDir(): before=%v, after=%v", before, after)
	}
}

func TestCreateSandboxTool_DriverFailureErrorsCleanly(t *testing.T) {
	dir := testutil.TempDir(t)
	driver := sandbox.NewFakeDriver()
	driver.CreateErr = errors.New("boom: driver refused to create")
	mgr := sandbox.NewManager(driver)
	tool := NewCreateSandboxTool(dir, mgr, alwaysApprove)

	res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]interface{}{}))
	if res.Error == "" {
		t.Fatal("expected an error when the driver fails to create the container")
	}
}
