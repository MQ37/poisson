package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildPX compiles the px binary once per test process into a temp dir and
// returns its path. The interactive TUI and provider setup can't run
// headlessly in a unit test, so `px resume` is exercised as a subprocess —
// this covers exactly the net-new fail-fast logic in main.go (missing arg,
// unknown session id), not the switch/hydrate itself (already covered by
// tui.TestCmdResume et al., which ResumeAtStartup delegates to).
func buildPX(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "px")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = "."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build px: %v\n%s", err, out)
	}
	return bin
}

// isolatedHome returns a HOME env pointing at a fresh temp dir, so
// config.ConfigDir()/store.Open() never touch the real ~/.poisson.
func isolatedHome(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// isolatedEnv builds a minimal, explicit environment for the subprocess:
// just PATH (so `go`/shared libs resolve) and the given HOME. Deliberately
// does NOT pass through os.Environ() — this test process may itself be
// running inside a poisson subagent (POISSON_SUBAGENT_CHILD=1 and friends),
// and inheriting that would make the built px binary take runChildMode()
// instead of normal CLI dispatch, silently breaking these assertions.
func isolatedEnv(home string) []string {
	return []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
}

func TestResumeCommand_MissingArg(t *testing.T) {
	bin := buildPX(t)
	cmd := exec.Command(bin, "resume")
	cmd.Env = isolatedEnv(isolatedHome(t))
	out, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected exit error, got %v (output: %s)", err, out)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2 (output: %s)", exitErr.ExitCode(), out)
	}
	if !strings.Contains(string(out), "usage: Poisson resume") {
		t.Errorf("output = %q, want usage message", out)
	}
}

func TestResumeCommand_SessionNotFound(t *testing.T) {
	bin := buildPX(t)
	cmd := exec.Command(bin, "resume", "nonexistent-session-id")
	cmd.Env = isolatedEnv(isolatedHome(t))
	out, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected exit error, got %v (output: %s)", err, out)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("exit code = %d, want 1 (output: %s)", exitErr.ExitCode(), out)
	}
	if !strings.Contains(string(out), "session not found: nonexistent-session-id") {
		t.Errorf("output = %q, want session-not-found message", out)
	}
}
