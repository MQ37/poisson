package subagent

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// --- buildSpawnArgs / buildSpawnEnv: pure, no process involved -------------
//
// These cover exactly the propagation logic behind a bug already found once
// this session ("subagent silently falls back to hardcoded model") —
// previously Spawn() inlined all of this with zero tests anywhere.

func TestBuildSpawnArgs(t *testing.T) {
	args := buildSpawnArgs(SpawnInput{SessionID: "sess-1", Task: "do the thing"})
	want := []string{"--json", "--session", "sess-1", "--", "do the thing"}
	if len(args) != len(want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args = %v, want %v", args, want)
		}
	}
}

// TestBuildSpawnArgsNoSkills guards the propagation this bug report was about:
// a parent session with skills disabled must produce a child argv carrying
// --no-skills, not silently give the child skills back.
func TestBuildSpawnArgsNoSkills(t *testing.T) {
	args := buildSpawnArgs(SpawnInput{SessionID: "sess-1", NoSkills: true})
	found := false
	for _, a := range args {
		if a == "--no-skills" {
			found = true
		}
	}
	if !found {
		t.Fatalf("args = %v, want --no-skills present when NoSkills is true", args)
	}
}

// TestBuildSpawnArgsSkillsEnabledByDefault guards the reverse: a parent with
// skills enabled (the common case) must NOT send --no-skills, since
// runChildMode now actually honors the flag (unlike before, when it was
// always sent but ignored).
func TestBuildSpawnArgsSkillsEnabledByDefault(t *testing.T) {
	args := buildSpawnArgs(SpawnInput{SessionID: "sess-1"})
	for _, a := range args {
		if a == "--no-skills" {
			t.Fatalf("args = %v, should not contain --no-skills when NoSkills is false", args)
		}
	}
}

func TestBuildSpawnArgsEmptyTaskOmitsSeparator(t *testing.T) {
	args := buildSpawnArgs(SpawnInput{SessionID: "sess-1"})
	for _, a := range args {
		if a == "--" {
			t.Fatalf("args = %v, should not append -- with no task", args)
		}
	}
}

func envValue(env []string, key string) (string, bool) {
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return strings.TrimPrefix(e, prefix), true
		}
	}
	return "", false
}

func TestBuildSpawnEnvPropagatesProviderModelEffort(t *testing.T) {
	env := buildSpawnEnv(SpawnInput{
		Provider: "anthropic",
		Model:    "claude-opus-4-8",
		Effort:   "high",
		Name:     "scout",
		DBPath:   "/tmp/child.db",
		Sandbox:  true,
	})

	cases := map[string]string{
		"POISSON_SUBAGENT_CHILD":    "1",
		"POISSON_SUBAGENT_PROVIDER": "anthropic",
		"POISSON_SUBAGENT_MODEL":    "claude-opus-4-8",
		"POISSON_SUBAGENT_EFFORT":   "high",
		"POISSON_SUBAGENT_NAME":     "scout",
		"POISSON_SUBAGENT_DB":       "/tmp/child.db",
		"POISSON_SANDBOX":           "1",
	}
	for key, want := range cases {
		got, ok := envValue(env, key)
		if !ok {
			t.Errorf("%s not set in child env", key)
			continue
		}
		if got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

// TestBuildSpawnEnvOmitsUnsetFields guards against the reverse bug: an empty
// Provider/Model/Effort/Name/DBPath must NOT produce a
// "POISSON_SUBAGENT_X=" (empty-but-present) variable, since runChildMode
// treats "" as "not set, fall back to config" — an accidentally-present
// empty var would silently override that fallback with an empty string
// instead of leaving it alone.
func TestBuildSpawnEnvOmitsUnsetFields(t *testing.T) {
	env := buildSpawnEnv(SpawnInput{})
	for _, key := range []string{"POISSON_SUBAGENT_PROVIDER", "POISSON_SUBAGENT_MODEL", "POISSON_SUBAGENT_EFFORT", "POISSON_SUBAGENT_NAME", "POISSON_SUBAGENT_DB", "POISSON_SANDBOX"} {
		if _, ok := envValue(env, key); ok {
			t.Errorf("%s should be absent when unset, but was present", key)
		}
	}
	if _, ok := envValue(env, "POISSON_SUBAGENT_CHILD"); !ok {
		t.Error("POISSON_SUBAGENT_CHILD must always be set")
	}
}

func TestBuildSpawnEnvInheritsParentEnvAndExtraEnv(t *testing.T) {
	t.Setenv("POISSON_SPAWN_TEST_MARKER", "present")
	env := buildSpawnEnv(SpawnInput{ExtraEnv: []string{"CUSTOM_VAR=xyz"}})
	if v, ok := envValue(env, "POISSON_SPAWN_TEST_MARKER"); !ok || v != "present" {
		t.Error("buildSpawnEnv must inherit the current process's environment")
	}
	if v, ok := envValue(env, "CUSTOM_VAR"); !ok || v != "xyz" {
		t.Error("buildSpawnEnv must include ExtraEnv")
	}
}

// --- Spawn() end-to-end: real exec/pipes, fake "child" script -------------
//
// lookupExecutable is overridden to run a tiny shell script instead of the
// real px binary — this exercises Spawn's actual process/pipe/env machinery
// (previously completely untested) with zero real LLM calls: the "child" is
// just a script that echoes canned JSON and reads one line back.

// writeFakeChildScript writes a shell script that: echoes its
// POISSON_SUBAGENT_PROVIDER/MODEL env vars back via ChildEvent's existing
// Command/Description fields (so the test can confirm Spawn really
// propagated them across the process boundary using fields the real
// protocol already has — ChildEvent carries no dedicated provider/model
// fields of its own), then emits a "retrying" event, reads one line from
// stdin (the approval response) and echoes it back as a "tool_result", then
// a final "done" event.
func writeFakeChildScript(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	path := dir + "/fake-child.sh"
	// The stdin line is a raw JSON object (double quotes and all); embedding
	// it verbatim inside another JSON string would need real escaping the
	// shell can't easily do, so instead just report whether it looks like an
	// approval by grepping for "approved":true — enough to prove the
	// round-trip through the real stdin pipe worked, without needing a JSON
	// escaper in shell.
	script := `#!/bin/sh
printf '{"type":"tool","tool":"echo-env","command":"%s","description":"%s"}\n' "$POISSON_SUBAGENT_PROVIDER" "$POISSON_SUBAGENT_MODEL"
printf '{"type":"retrying","text":"connection lost - reconnecting"}\n'
read -r line
if echo "$line" | grep -q '"approved":true'; then
  printf '{"type":"tool_result","result":"saw-approved"}\n'
else
  printf '{"type":"tool_result","result":"saw-other"}\n'
fi
printf '{"type":"done","success":true}\n'
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake child script: %v", err)
	}
	return path
}

func withFakeExecutable(t *testing.T, path string) {
	t.Helper()
	restore := SetLookupExecutableForTest(path)
	t.Cleanup(restore)
}

func TestSpawnEndToEndWithFakeChildProcess(t *testing.T) {
	scriptPath := writeFakeChildScript(t)
	withFakeExecutable(t, scriptPath)

	child, err := Spawn(SpawnInput{
		Task:      "do something",
		Cwd:       ".",
		SessionID: "sess-1",
		Provider:  "anthropic",
		Model:     "claude-opus-4-8",
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer child.Reap()

	// First event: proves Provider/Model really crossed the process boundary
	// via real env vars, not just in-memory struct fields.
	ev, err := child.ReadEvent()
	if err != nil {
		t.Fatalf("ReadEvent (tool): %v", err)
	}
	if ev == nil || ev.Tool != "echo-env" {
		t.Fatalf("event = %+v, want tool=echo-env", ev)
	}
	if ev.Command != "anthropic" || ev.Description != "claude-opus-4-8" {
		t.Fatalf("child saw provider=%q model=%q via env, want anthropic/claude-opus-4-8", ev.Command, ev.Description)
	}

	ev, err = child.ReadEvent()
	if err != nil || ev == nil || ev.Type != "retrying" {
		t.Fatalf("ReadEvent (retrying) = %+v, err=%v", ev, err)
	}

	// Round-trip stdin -> stdout through the real pipes SendApprovalSafe uses.
	if err := child.SendApprovalSafe(true, ""); err != nil {
		t.Fatalf("SendApprovalSafe: %v", err)
	}

	ev, err = child.ReadEvent()
	if err != nil || ev == nil || ev.Type != "tool_result" {
		t.Fatalf("ReadEvent (tool_result) = %+v, err=%v", ev, err)
	}
	if ev.Result != "saw-approved" {
		t.Errorf("tool_result = %q, want saw-approved (the real stdin pipe should have carried SendApprovalSafe's approved:true)", ev.Result)
	}

	ev, err = child.ReadEvent()
	if err != nil || ev == nil || ev.Type != "done" || !ev.Success {
		t.Fatalf("ReadEvent (done) = %+v, err=%v", ev, err)
	}

	if err := child.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestSpawnPropagatesProviderAndModelAcrossRealProcessBoundary(t *testing.T) {
	scriptPath := writeFakeChildScript(t)
	withFakeExecutable(t, scriptPath)

	for _, tc := range []struct{ provider, model string }{
		{"xai", "grok-code"},
		{"ollama", "kimi-k2.7-code"},
	} {
		child, err := Spawn(SpawnInput{Task: "t", Cwd: ".", SessionID: "s", Provider: tc.provider, Model: tc.model})
		if err != nil {
			t.Fatalf("Spawn: %v", err)
		}
		ev, err := child.ReadEvent()
		if err != nil || ev == nil {
			t.Fatalf("ReadEvent: %v, %+v", err, ev)
		}
		if ev.Command != tc.provider || ev.Description != tc.model {
			t.Errorf("child saw provider=%q model=%q via env, want %s/%s", ev.Command, ev.Description, tc.provider, tc.model)
		}
		child.Kill()
		child.Wait()
	}
}
