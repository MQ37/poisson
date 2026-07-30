package main

import (
	"encoding/json"
	"testing"

	"github.com/mq37/poisson/internal/subagent"
)

// TestResolveChildSandboxManager_Empty covers the common case: a subagent
// spawned with no sandboxes authorized (POISSON_SUBAGENT_SANDBOXES unset,
// buildSpawnEnv never emits it — see subagent.buildSpawnEnv) must get a nil
// Manager, not an empty-but-non-nil one — build.go only registers
// sandbox_cp/sandbox_destroy when SandboxManager != nil, so nil here is
// what keeps those tools off a plain child's registry entirely.
func TestResolveChildSandboxManager_Empty(t *testing.T) {
	if mgr := resolveChildSandboxManager(""); mgr != nil {
		t.Errorf("resolveChildSandboxManager(\"\") = %v, want nil", mgr)
	}
}

// TestResolveChildSandboxManager_Malformed: unparseable input must be
// treated the same as none authorized — never let a subagent attempt to
// use sandboxing off of input that didn't actually come from
// subagent.buildSpawnEnv.
func TestResolveChildSandboxManager_Malformed(t *testing.T) {
	if mgr := resolveChildSandboxManager("not valid json"); mgr != nil {
		t.Errorf("resolveChildSandboxManager(malformed) = %v, want nil", mgr)
	}
}

// TestResolveChildSandboxManager_Authorizes proves the real wiring: what a
// parent's buildSpawnEnv would have produced for one authorized sandbox
// results in a Manager that owns exactly that id with its hostPath intact.
func TestResolveChildSandboxManager_Authorizes(t *testing.T) {
	authorized := []subagent.SandboxAuth{{ID: "abc123", HostPath: "/tmp/poisson-sandbox-abc123/workspace"}}
	raw, err := json.Marshal(authorized)
	if err != nil {
		t.Fatal(err)
	}

	mgr := resolveChildSandboxManager(string(raw))
	if mgr == nil {
		t.Fatal("resolveChildSandboxManager returned nil, want a Manager owning the authorized sandbox")
	}
	if !mgr.Owns("abc123") {
		t.Fatal("Manager does not own the authorized sandboxId")
	}
	sb, ok := mgr.Get("abc123")
	if !ok || sb.HostPath != "/tmp/poisson-sandbox-abc123/workspace" {
		t.Errorf("Get(\"abc123\") = %+v, %v, want matching HostPath", sb, ok)
	}
	if mgr.Owns("some-other-id") {
		t.Error("Manager should not own an id that was never authorized")
	}
}
