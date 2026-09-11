package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/subagent"
	"github.com/mq37/poisson/internal/tools"
)

// jobIDFromAckForTest extracts the async job id from the subagent tool's
// spawn ack — a package-local copy of tools' own (unexported, cross-package
// inaccessible) jobIDFromAck test helper, since these tests exercise the
// real SubagentTool/SubagentStatusTool/SubagentResultTool from outside
// package tools, the same way production code does.
var subagentAckJobIDRe = regexp.MustCompile(`spawned as job (\S+)\.`)

func jobIDFromAckForTest(t *testing.T, content string) string {
	t.Helper()
	m := subagentAckJobIDRe.FindStringSubmatch(content)
	if m == nil {
		t.Fatalf("ack text has no job id: %q", content)
	}
	return m[1]
}

// jobResultForTest polls subagent_status until jobID is done/error, then
// retrieves it via subagent_result — the only way to observe a job's
// outcome from outside package tools (SubagentTool's job map is
// unexported), matching how the model itself would actually do it.
func jobResultForTest(t *testing.T, statusTool, resultTool provider.Tool, jobID string) tools.ToolResult {
	t.Helper()
	input := json.RawMessage(fmt.Sprintf(`{"jobId":%q}`, jobID))
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		res, err := statusTool.Execute(context.Background(), input)
		if err != nil {
			t.Fatalf("subagent_status returned a Go error: %v", err)
		}
		if strings.Contains(res.Content, "status: done") || strings.Contains(res.Content, "status: error") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	res, err := resultTool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("subagent_result returned a Go error: %v", err)
	}
	return res
}

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
//
// Execute itself now only returns an async spawn ack (see
// docs/async-subagent-plan.md) — the outcome is observed via
// subagent_status/subagent_result instead, the same way a model actually
// would. An earlier version of this test read Execute's own return value
// directly, which the async rework silently turned into a false-positive
// pass: Execute returns almost instantly regardless of whether expedite
// ever reached the child at all.
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
	statusTool := tools.NewSubagentStatusTool(st)
	resultTool := tools.NewSubagentResultTool(st)

	res, err := st.Execute(context.Background(), json.RawMessage(`{"task":"do something"}`))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("Execute reported an error on the async spawn ack: %q", res.Error)
	}
	jobID := jobIDFromAckForTest(t, res.Content)

	// The child blocks reading its own stdin until SendExpedite fires, so
	// polling here is only about waiting for the background job to have
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

	// jobResultForTest's own polling loop is the real assertion: it only
	// returns once subagent_status reports "done" (or errors after 5s) — the
	// child's "tool_result" event type is a no-op in runJob's event switch
	// (never captured into the job's output text, same as before this
	// file's rewrite), so there is no "expedited" substring to look for
	// downstream. Reaching "done" at all proves the child's blocking stdin
	// read actually unblocked because of the expedite signal, not that it
	// hung until this test's own deadline gave up.
	final := jobResultForTest(t, statusTool, resultTool, jobID)
	if final.Error != "" {
		t.Fatalf("job reported an error: %q", final.Error)
	}
}

// TestKillSubagentsNoSubagentToolRegistered mirrors
// TestExpediteSubagentsNoSubagentToolRegistered for KillSubagents.
func TestKillSubagentsNoSubagentToolRegistered(t *testing.T) {
	e := newIntegEnv(t, nil)
	if got := e.agent.KillSubagents(); got != 0 {
		t.Fatalf("KillSubagents() = %d, want 0 (no \"subagent\" tool registered)", got)
	}
}

// TestKillSubagentsReachesLiveChild proves the full chain end-to-end at the
// agent layer, mirroring TestExpediteSubagentsReachesLiveChild: a real live
// child stuck in a long sleep (standing in for a subagent mid-run when px
// itself is shutting down — see TUI.prepareShutdownLocked) must actually be
// terminated by Agent.KillSubagents, not just have KillAll report a count.
func TestKillSubagentsReachesLiveChild(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	e := newIntegEnv(t, nil)

	scriptPath := e.dir + "/fake-child-kill.sh"
	script := "#!/bin/sh\nsleep 30\nprintf '{\"type\":\"done\",\"success\":true}\\n'\n"
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
	statusTool := tools.NewSubagentStatusTool(st)
	resultTool := tools.NewSubagentResultTool(st)

	res, err := st.Execute(context.Background(), json.RawMessage(`{"task":"do something"}`))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	jobID := jobIDFromAckForTest(t, res.Content)

	// Unlike ExpediteAll (a repeatable, side-effect-free nudge), KillAll now
	// also proactively cancels every non-terminal job's own context on its
	// very first call (see the fix for the "shutdown spawns an orphan"
	// bug) — a polling loop that calls KillSubagents itself would cancel
	// this job's context before the child ever gets a chance to actually
	// spawn and go live, since "queued" already counts as non-terminal.
	// Poll subagent_status instead (side-effect-free) until the child is
	// confirmed live, then call KillSubagents exactly once — matching how
	// it's actually used in production (TUI.prepareShutdownLocked, a single
	// one-shot call at process exit).
	statusInput := json.RawMessage(fmt.Sprintf(`{"jobId":%q}`, jobID))
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		res, err := statusTool.Execute(context.Background(), statusInput)
		if err != nil {
			t.Fatalf("subagent_status returned a Go error: %v", err)
		}
		if strings.Contains(res.Content, "status: running") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	got := e.agent.KillSubagents()
	if got == 0 {
		t.Fatal("KillSubagents() never signalled the live child")
	}

	final := jobResultForTest(t, statusTool, resultTool, jobID)
	if final.Error == "" {
		t.Fatalf("job result = %+v, want an error — the child was killed, not left to finish its 30s sleep", final)
	}
}
