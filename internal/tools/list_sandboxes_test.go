package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/sandbox"
)

func TestListSandboxesTool_NoManagerConfigured(t *testing.T) {
	tool := NewListSandboxesTool(nil)
	res, _ := tool.Execute(context.Background(), nil)
	if res.Error == "" || !strings.Contains(res.Error, "not available") {
		t.Fatalf("error = %q, want a clear 'not available' message", res.Error)
	}
}

func TestListSandboxesTool_Empty(t *testing.T) {
	mgr := sandbox.NewManager(sandbox.NewFakeDriver())
	tool := NewListSandboxesTool(mgr)
	res, _ := tool.Execute(context.Background(), nil)
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if res.Content != "no live sandboxes" {
		t.Errorf("Content = %q, want the empty-list message", res.Content)
	}
}

// TestListSandboxesTool_CrossSessionVisibility is the core discovery
// property: a sandbox created via one Manager shows up in another,
// independent Manager's list_sandboxes call, as long as they share the same
// underlying driver (i.e. the same real podman backend) — the exact shape
// of "session A crashed, session B lists sandboxes and finds A's".
func TestListSandboxesTool_CrossSessionVisibility(t *testing.T) {
	driver := sandbox.NewFakeDriver()
	sessionA := sandbox.NewManager(driver)
	sessionA.SetSessionID("session-a")
	if _, err := sessionA.Create(context.Background(), sandbox.CreateOpts{Name: "api-testing-2", HostPath: "/tmp/x/workspace"}); err != nil {
		t.Fatal(err)
	}

	sessionB := sandbox.NewManager(driver) // a fresh/independent Manager, as if a different process
	tool := NewListSandboxesTool(sessionB)
	res, _ := tool.Execute(context.Background(), nil)
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}

	var entries []struct {
		SandboxID string `json:"sandboxId"`
		HostPath  string `json:"hostPath"`
		SessionID string `json:"sessionId"`
		Running   bool   `json:"running"`
	}
	if err := json.Unmarshal([]byte(res.Content), &entries); err != nil {
		t.Fatalf("unmarshal: %v (content=%q)", err, res.Content)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want exactly 1", entries)
	}
	e := entries[0]
	if e.SandboxID != "px-sandbox-api-testing-2" || e.HostPath != "/tmp/x/workspace" || e.SessionID != "session-a" || !e.Running {
		t.Errorf("entry = %+v, did not match what session A created", e)
	}
}

// TestListSandboxesTool_BrowsingGrantsNoAccess confirms list_sandboxes
// showing an entry doesn't itself let a discovery-disabled Manager act on
// it — Owns/Get still gate bash/sandbox_cp/sandbox_destroy independently
// (see docs/sandbox-plan.md's "Crash recovery" section).
func TestListSandboxesTool_BrowsingGrantsNoAccess(t *testing.T) {
	driver := sandbox.NewFakeDriver()
	owner := sandbox.NewManager(driver)
	sb, err := owner.Create(context.Background(), sandbox.CreateOpts{Name: "someone-elses"})
	if err != nil {
		t.Fatal(err)
	}

	stranger := sandbox.NewManager(driver) // discovery never enabled
	tool := NewListSandboxesTool(stranger)
	res, _ := tool.Execute(context.Background(), nil)
	if !strings.Contains(res.Content, sb.ID) {
		t.Fatalf("expected the listing to include %q, got %q", sb.ID, res.Content)
	}
	if stranger.Owns(sb.ID) {
		t.Error("seeing a sandbox in the listing must not itself grant ownership/access")
	}
}
