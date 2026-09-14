package main

import "testing"

// TestBuildPrintAgent_OrchestrateEnvHidesSetTitle drives buildPrintAgent
// in-process (no network call is made -- provider construction alone never
// touches the wire) to check POISSON_ORCHESTRATE_INSTANCE=1 threads through
// to tools.BuildOptions.NoSetTitle (docs/orchestrator-host-mode-plan.md
// Step H2). Uses an isolated HOME so config.ConfigDir()/store.Open() never
// touch the real ~/.poisson.
func TestBuildPrintAgent_OrchestrateEnvHidesSetTitle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	t.Setenv("POISSON_ORCHESTRATE_INSTANCE", "1")
	build, err := buildPrintAgent(printOpts{print: true, jsonOut: true, prompt: "hi"})
	if err != nil {
		t.Fatalf("buildPrintAgent: %v", err)
	}
	defer build.cleanup()
	for _, name := range build.agent.ToolNames() {
		if name == "set_title" {
			t.Error("POISSON_ORCHESTRATE_INSTANCE=1: agent has set_title, want omitted")
		}
	}

	t.Setenv("POISSON_ORCHESTRATE_INSTANCE", "")
	build2, err := buildPrintAgent(printOpts{print: true, jsonOut: true, prompt: "hi", sessionID: "other"})
	if err != nil {
		t.Fatalf("buildPrintAgent (unset): %v", err)
	}
	defer build2.cleanup()
	found := false
	for _, name := range build2.agent.ToolNames() {
		if name == "set_title" {
			found = true
		}
	}
	if !found {
		t.Error("POISSON_ORCHESTRATE_INSTANCE unset: agent missing set_title, want present")
	}
}
