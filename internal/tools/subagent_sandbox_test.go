package tools

import (
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/sandbox"
	"github.com/mq37/poisson/internal/subagent"
)

// TestSubagentTool_SandboxIdsRequireManagerConfigured: a session with no
// sandbox support at all (the common case today — nothing production-side
// constructs a real Manager yet) must reject a sandboxIds request clearly,
// before it ever gets near spawning anything.
func TestSubagentTool_SandboxIdsRequireManagerConfigured(t *testing.T) {
	tool := NewSubagentTool(".", alwaysApproveSubagent)
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"task": "do something", "sandboxIds": []string{"anything"},
	}))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if res.Error == "" || !strings.Contains(res.Error, "not available") {
		t.Fatalf("error = %q, want a clear 'not available' message", res.Error)
	}
}

// TestSubagentTool_SandboxIdsRejectsForeignID: same untrusted-input
// discipline as bash's own sandboxId handling — a hallucinated or foreign
// id must fail the whole spawn loudly, not be silently dropped.
func TestSubagentTool_SandboxIdsRejectsForeignID(t *testing.T) {
	mgr := sandbox.NewManager(sandbox.NewFakeDriver())
	tool := NewSubagentTool(".", alwaysApproveSubagent)
	tool.SetSandboxManager(mgr)

	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"task": "do something", "sandboxIds": []string{"fake-999"},
	}))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if res.Error == "" || !strings.Contains(res.Error, "not found") {
		t.Fatalf("error = %q, want a 'not found' message for a foreign sandboxId", res.Error)
	}
}

// TestSubagentTool_SandboxIdsPropagateToChildEnvironment is the full
// round-trip: an owned sandboxId resolves against this session's Manager,
// survives subagent.Spawn's env-var propagation, and actually reaches the
// child PROCESS's environment as the correct JSON — with a real spawned
// process (a fake shell "child" script, not the real px binary), same
// pattern as TestSubagentToolRelaysRetryingStatusEndToEnd. The fake child
// reports the value back via an "approval_request" event's Command field —
// the one ChildEvent field SubagentTool.Execute's event loop actually reads
// out (a plain "tool" event's Command/Description are relayed-but-ignored,
// see the "tool" case) — captured here via a spy SubagentApproval instead of
// through ToolResult.Content. base64-encoded first: the raw value is itself
// a JSON string, and embedding its quotes verbatim inside another JSON
// string via shell printf would produce invalid JSON.
func TestSubagentTool_SandboxIdsPropagateToChildEnvironment(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	if _, err := exec.LookPath("base64"); err != nil {
		t.Skip("base64 not available")
	}
	mgr := sandbox.NewManager(sandbox.NewFakeDriver())
	sb, err := mgr.Create(context.Background(), sandbox.CreateOpts{HostPath: "/tmp/poisson-sandbox-xyz/workspace"})
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	scriptPath := dir + "/fake-child.sh"
	script := `#!/bin/sh
encoded=$(printf '%s' "$POISSON_SUBAGENT_SANDBOXES" | base64 | tr -d '\n')
printf '{"type":"approval_request","command":"%s","description":"echo-env"}\n' "$encoded"
read -r line
printf '{"type":"done","success":true}\n'
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake child script: %v", err)
	}
	restore := subagent.SetLookupExecutableForTest(scriptPath)
	defer restore()

	var seenCommand string
	spy := func(command, description, workdir, agentName, risk string) (bool, string) {
		seenCommand = command
		return true, ""
	}

	tool := NewSubagentTool(".", spy)
	tool.SetSandboxManager(mgr)
	tool.SetRuntime(
		func() string { return "anthropic" },
		func() string { return "claude-opus-5" },
		func() string { return "" },
	)

	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"task": "do something", "sandboxIds": []string{sb.ID},
	}))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("Execute reported an error: %q (content=%q)", res.Error, res.Content)
	}
	if seenCommand == "" {
		t.Fatal("approvalFn spy never saw an approval_request from the fake child")
	}

	decoded, err := base64.StdEncoding.DecodeString(seenCommand)
	if err != nil {
		t.Fatalf("base64-decode echoed env value %q: %v", seenCommand, err)
	}
	got, err := subagent.ParseAuthorizedSandboxes(string(decoded))
	if err != nil {
		t.Fatalf("ParseAuthorizedSandboxes(%q): %v", decoded, err)
	}
	if len(got) != 1 || got[0].ID != sb.ID || got[0].HostPath != sb.HostPath {
		t.Fatalf("child saw %+v, want [{%s %s}]", got, sb.ID, sb.HostPath)
	}
}
