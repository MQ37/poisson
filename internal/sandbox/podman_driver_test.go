package sandbox

import "testing"

// TestCreateArgsIncludesInit is the regression guard for orphaned zombie
// processes: without --init, the container's PID 1 (`sleep infinity`) never
// calls wait() on anything, so a process a bash tool call or subagent
// backgrounds (or leaves running when killed) reparents to PID 1 on exit
// and stays a zombie for the container's entire lifetime — sandboxes are
// designed to be long-lived/crash-surviving, so that's unbounded. --init
// (podman's built-in catatonit) makes PID 1 actually reap orphans.
func TestCreateArgsIncludesInit(t *testing.T) {
	args := createArgs("test-container", CreateOpts{Image: "ubuntu:26.04"})
	found := false
	for _, a := range args {
		if a == "--init" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("createArgs = %v, missing --init", args)
	}
	if args[0] != "create" {
		t.Fatalf("createArgs[0] = %q, want %q", args[0], "create")
	}
}
