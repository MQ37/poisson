package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/sandbox"
)

func TestSandboxResurrectTool_NoManagerConfigured(t *testing.T) {
	tool := NewSandboxResurrectTool(nil)
	res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]interface{}{"sandboxId": "x"}))
	if res.Error == "" || !strings.Contains(res.Error, "not available") {
		t.Fatalf("error = %q, want a clear 'not available' message", res.Error)
	}
}

func TestSandboxResurrectTool_MissingSandboxID(t *testing.T) {
	mgr := sandbox.NewManager(sandbox.NewFakeDriver())
	tool := NewSandboxResurrectTool(mgr)
	res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]interface{}{}))
	if res.Error == "" || !strings.Contains(res.Error, "sandboxId is required") {
		t.Fatalf("error = %q, want a 'sandboxId is required' message", res.Error)
	}
}

// TestSandboxResurrectTool_ResumesOwnedStopped is the core happy path: a
// sandbox this session created, then stopped, comes back via
// sandbox_resurrect with the same hostPath.
func TestSandboxResurrectTool_ResumesOwnedStopped(t *testing.T) {
	driver := sandbox.NewFakeDriver()
	mgr := sandbox.NewManager(driver)
	sb, err := mgr.Create(context.Background(), sandbox.CreateOpts{Name: "resume-me", HostPath: "/tmp/x/workspace"})
	if err != nil {
		t.Fatal(err)
	}
	driver.MarkDead(sb.ID)

	tool := NewSandboxResurrectTool(mgr)
	res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]interface{}{"sandboxId": sb.ID}))
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if !strings.Contains(res.Content, sb.ID) || !strings.Contains(res.Content, "/tmp/x/workspace") {
		t.Errorf("Content = %q, want it to include the sandboxId and hostPath", res.Content)
	}
	if alive, err := mgr.Alive(context.Background(), sb.ID); err != nil || !alive {
		t.Errorf("Alive after resurrect = %v, %v, want true, nil", alive, err)
	}
}

// TestSandboxResurrectTool_ResumesViaDiscovery covers the actual reported
// scenario: a fresh session (as if px restarted) resurrecting a sandbox it
// never itself created, found only through cross-session discovery.
func TestSandboxResurrectTool_ResumesViaDiscovery(t *testing.T) {
	driver := sandbox.NewFakeDriver()
	owner := sandbox.NewManager(driver)
	sb, err := owner.Create(context.Background(), sandbox.CreateOpts{Name: "discovered-resume"})
	if err != nil {
		t.Fatal(err)
	}
	driver.MarkDead(sb.ID)

	fresh := sandbox.NewManager(driver) // simulates a restarted process
	fresh.EnableDiscovery()
	tool := NewSandboxResurrectTool(fresh)

	res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]interface{}{"sandboxId": sb.ID}))
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if !fresh.Owns(sb.ID) {
		t.Error("fresh Manager should own the sandbox after resurrecting it")
	}
}

func TestSandboxResurrectTool_UnknownIDErrors(t *testing.T) {
	mgr := sandbox.NewManager(sandbox.NewFakeDriver())
	tool := NewSandboxResurrectTool(mgr)
	res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]interface{}{"sandboxId": "never-existed"}))
	if res.Error == "" {
		t.Fatal("expected an error for an unknown sandboxId")
	}
}
