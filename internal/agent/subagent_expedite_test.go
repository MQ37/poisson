package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/subagent"
	"github.com/mq37/poisson/internal/tools"
)

// TestExpediteSubagentsNoSubagentToolRegistered covers the negative path that
// had zero test either: an Agent whose registry never registered a
// "subagent" tool at all (newIntegEnv only registers read/write/bash) must
// return 0 from ExpediteSubagents, not panic on the Get/type-assertion path.
func TestExpediteSubagentsNoSubagentToolRegistered(t *testing.T) {
	e := newIntegEnv(t, nil)
	if got := e.agent.ExpediteSubagents(); got != 0 {
		t.Fatalf("ExpediteSubagents() = %d, want 0 (no \"subagent\" tool registered)", got)
	}
}

// TestExpediteSubagentsReachesLiveChild proves the full chain end-to-end at
// the agent layer: a real *tools.SubagentTool registered under "subagent",
// with a real live child (spawned via the same fake-child-process pattern
// used throughout internal/subagent and internal/tools — a shell script
// written via os.WriteFile with the executable bit, pointed at via
// subagent.SetLookupExecutableForTest). Agent.ExpediteSubagents must find
// the tool, succeed the t.(*tools.SubagentTool) type assertion, and reach
// ExpediteAll, returning a nonzero count — and the live child, blocked
// reading its own stdin, must actually unblock and finish because of it.
func TestExpediteSubagentsReachesLiveChild(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	e := newIntegEnv(t, nil)

	scriptPath := e.dir + "/fake-child-expedite.sh"
	script := `#!/bin/sh
read -r line
if echo "$line" | grep -q '"type":"expedite"'; then
  printf '{"type":"tool_result","result":"expedited"}\n'
else
  printf '{"type":"tool_result","result":"other"}\n'
fi
printf '{"type":"done","success":true}\n'
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake child script: %v", err)
	}
	restore := subagent.SetLookupExecutableForTest(scriptPath)
	defer restore()

	st := tools.NewSubagentTool(e.dir, func(_, _, _, _, _ string) (bool, string) { return true, "" })
	st.SetRuntime(
		func() string { return "anthropic" },
		func() string { return "claude-opus-5" },
		func() string { return "" },
	)
	e.reg.Register(st)

	done := make(chan tools.ToolResult, 1)
	go func() {
		res, err := st.Execute(context.Background(), json.RawMessage(`{"task":"do something"}`))
		if err != nil {
			t.Errorf("Execute returned a Go error: %v", err)
		}
		done <- res
	}()

	// The child blocks reading its own stdin until SendExpedite fires, so
	// polling here is only about waiting for Execute's goroutine to have
	// actually tracked the child yet — not a race on the expedite itself
	// (a write into the child's stdin pipe is buffered regardless of
	// whether the child has reached its `read` yet).
	deadline := time.Now().Add(5 * time.Second)
	var got int
	for time.Now().Before(deadline) {
		got = e.agent.ExpediteSubagents()
		if got > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got == 0 {
		t.Fatal("ExpediteSubagents() never signalled the live child within 5s")
	}

	select {
	case res := <-done:
		if res.Error != "" {
			t.Fatalf("Execute reported an error: %q", res.Error)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Execute never returned after being expedited")
	}
}
