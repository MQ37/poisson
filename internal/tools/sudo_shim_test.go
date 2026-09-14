package tools

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/testutil"
)

// testAskpassRelayEnv, when set to "1", makes TestMain re-exec this test
// binary itself as the sudo askpass relay instead of running tests — the
// same "helper process" pattern the Go standard library's own os/exec
// tests use. Needed because buildAskpassRelayScript normally points
// SUDO_ASKPASS at this process's own binary implementing
// `--internal-sudo-askpass` (see cmd/px), which the compiled *test*
// binary has no such dispatch for; overriding buildAskpassRelayScript to
// re-exec the test binary with this env var set gives it one, without
// needing to build or depend on any other binary.
const testAskpassRelayEnv = "POISSON_TEST_ASKPASS_RELAY"

func TestMain(m *testing.M) {
	if os.Getenv(testAskpassRelayEnv) == "1" {
		runTestAskpassRelay(os.Args[len(os.Args)-1])
		return
	}
	os.Exit(m.Run())
}

// runTestAskpassRelay mirrors cmd/px's real runSudoAskpassRelay: connect to
// sockPath, block for a password, print it to stdout, exit nonzero on any
// failure — exactly what a real sudo -A expects from its askpass helper.
func runTestAskpassRelay(sockPath string) {
	conn, err := net.DialTimeout("unix", sockPath, 5*time.Second)
	if err != nil {
		os.Exit(1)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	password, err := io.ReadAll(conn)
	if err != nil || len(password) == 0 {
		os.Exit(1)
	}
	os.Stdout.Write(password)
	os.Exit(0)
}

// useTestAskpassRelay points buildAskpassRelayScript at this test binary's
// own re-exec helper (see TestMain) for the duration of one test, instead
// of the real os.Executable()-based relay cmd/px wires up.
func useTestAskpassRelay(t *testing.T) {
	t.Helper()
	self := os.Args[0]
	orig := buildAskpassRelayScript
	buildAskpassRelayScript = func(sockPath string) (string, error) {
		return "#!/bin/sh\n" + testAskpassRelayEnv + "=1 exec " + shellSingleQuote(self) + " " + shellSingleQuote(sockPath) + "\n", nil
	}
	t.Cleanup(func() { buildAskpassRelayScript = orig })
}

func TestNewSudoShim_WritesFilesAndCleansUp(t *testing.T) {
	useTestAskpassRelay(t)
	// exec.LookPath("sudo") must resolve to something for newSudoShim to
	// proceed — a fake on PATH is enough, its content is irrelevant here.
	fakeDir := t.TempDir()
	fakeSudo := filepath.Join(fakeDir, "sudo")
	if err := os.WriteFile(fakeSudo, []byte("#!/bin/sh\ntrue\n"), 0o700); err != nil {
		t.Fatalf("write fake sudo: %v", err)
	}
	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", fakeDir+string(os.PathListSeparator)+oldPath)
	defer os.Setenv("PATH", oldPath)

	ask := func(ctx context.Context, command, description, workdir string) ([]byte, bool) {
		return []byte("unused"), true
	}
	s, err := newSudoShim(context.Background(), ask, "cmd", "desc", "")
	if err != nil {
		t.Fatalf("newSudoShim: %v", err)
	}

	dirInfo, err := os.Stat(s.dir)
	if err != nil || dirInfo.Mode().Perm() != 0o700 {
		t.Errorf("shim dir perm = %v (err=%v), want 0700", dirInfo, err)
	}
	sudoScript, err := os.ReadFile(filepath.Join(s.dir, "sudo"))
	if err != nil {
		t.Fatalf("read shim sudo script: %v", err)
	}
	if !strings.Contains(string(sudoScript), fakeSudo) || !strings.Contains(string(sudoScript), "-A -k") {
		t.Errorf("shim sudo script = %q, want it to exec %q with -A -k", sudoScript, fakeSudo)
	}
	askpassScript, err := os.ReadFile(s.askpassSh)
	if err != nil {
		t.Fatalf("read askpass relay script: %v", err)
	}
	if !strings.Contains(string(askpassScript), testAskpassRelayEnv) {
		t.Errorf("askpass script = %q, want the test relay override applied", askpassScript)
	}

	env := s.buildEnv()
	var sawPath, sawAskpass bool
	pathCount, askpassCount := 0, 0
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			pathCount++
			if strings.HasPrefix(kv, "PATH="+s.dir+string(os.PathListSeparator)) {
				sawPath = true
			}
		}
		if strings.HasPrefix(kv, "SUDO_ASKPASS=") {
			askpassCount++
			if kv == "SUDO_ASKPASS="+s.askpassSh {
				sawAskpass = true
			}
		}
	}
	if pathCount != 1 || !sawPath {
		t.Errorf("buildEnv PATH entries = %d (want exactly 1, shim dir first), sawPath=%v", pathCount, sawPath)
	}
	if askpassCount != 1 || !sawAskpass {
		t.Errorf("buildEnv SUDO_ASKPASS entries = %d (want exactly 1, pointing at shim script), sawAskpass=%v", askpassCount, sawAskpass)
	}

	s.close()
	if _, err := os.Stat(s.dir); !os.IsNotExist(err) {
		t.Errorf("shim dir still exists after close: err=%v", err)
	}
}

func TestNewSudoShim_NoSudoBinaryFails(t *testing.T) {
	emptyDir := t.TempDir()
	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", emptyDir) // no sudo anywhere on it
	defer os.Setenv("PATH", oldPath)

	ask := func(ctx context.Context, command, description, workdir string) ([]byte, bool) {
		t.Fatal("ask should never be called")
		return nil, false
	}
	if _, err := newSudoShim(context.Background(), ask, "cmd", "desc", ""); err == nil {
		t.Fatal("newSudoShim with no sudo on PATH should fail, got nil error")
	}
}

// TestBashTool_SudoShim_CatchesHiddenLocalSudo is the actual regression this
// tier closes: `sh -c 'sudo whoami'` has no bare top-level "sudo" token, so
// guard.RequiresSudoPassword (Tier 1) does not fire — before this shim, the
// command would just hit a real (fake, here) sudo with no -A/SUDO_ASKPASS
// wired at all and fail with no controlling terminal. The shim's PATH entry
// intercepts it regardless of how deeply nested the actual "sudo" word is.
func TestBashTool_SudoShim_CatchesHiddenLocalSudo(t *testing.T) {
	useTestAskpassRelay(t)
	fakeDir := t.TempDir()
	fakeSudo := filepath.Join(fakeDir, "sudo")
	script := `#!/bin/sh
if [ "$1" != "-A" ] || [ "$2" != "-k" ]; then
	echo "fake sudo: expected -A -k, got: $*" >&2
	exit 1
fi
shift 2
pw=$("$SUDO_ASKPASS")
if [ "$pw" != "hunter2" ]; then
	echo "fake sudo: wrong password" >&2
	exit 1
fi
exec "$@"
`
	if err := os.WriteFile(fakeSudo, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake sudo: %v", err)
	}
	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", fakeDir+string(os.PathListSeparator)+oldPath)
	defer os.Setenv("PATH", oldPath)

	dir := testutil.TempDir(t)
	b := NewBashTool(dir, alwaysApprove)
	var asked bool
	b.SetSudoPasswordFn(func(ctx context.Context, command, description, workdir string) ([]byte, bool) {
		asked = true
		return []byte("hunter2"), true
	})

	command := `sh -c 'sudo whoami'`
	res, err := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command": command, "description": "hidden sudo behind sh -c",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	var out bashOutput
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.ExitCode != 0 {
		t.Fatalf("command failed: exitCode=%d stdout=%q stderr=%q", out.ExitCode, out.Stdout, out.Stderr)
	}
	if !asked {
		t.Error("sudoPasswordFn was never called — shim didn't intercept the hidden sudo")
	}
	if strings.Contains(out.Stdout, "hunter2") || strings.Contains(out.Stderr, "hunter2") {
		t.Errorf("password leaked into tool output: stdout=%q stderr=%q", out.Stdout, out.Stderr)
	}
}

// TestBashTool_SudoShim_NeverFiresForPlainCommand proves the shim stays
// silent (never even asks) when the command never touches sudo at all —
// exactly the "don't ask when it's not needed" requirement.
func TestBashTool_SudoShim_NeverFiresForPlainCommand(t *testing.T) {
	useTestAskpassRelay(t)
	dir := testutil.TempDir(t)
	b := NewBashTool(dir, alwaysApprove)
	var asked bool
	b.SetSudoPasswordFn(func(ctx context.Context, command, description, workdir string) ([]byte, bool) {
		asked = true
		return []byte("hunter2"), true
	})

	res, err := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command": "echo hello", "description": "plain command",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if asked {
		t.Error("sudoPasswordFn was called for a command with no sudo anywhere in it")
	}
}

// TestBashTool_SudoShim_NeverFiresForRemoteHeredocSudo pins the exact bug
// report this whole fix responds to: a heredoc-delimited remote command
// containing "sudo" must never trigger a local password prompt. ssh itself
// isn't invoked for real here (no network, no remote host) — this only
// needs to prove RequiresSudoPassword (Tier 1, via the guard.Segments
// heredoc fix) doesn't fire and the Tier 2 shim never asks either, since
// no LOCAL sudo actually runs in this command.
func TestBashTool_SudoShim_NeverFiresForRemoteHeredocSudo(t *testing.T) {
	useTestAskpassRelay(t)
	dir := testutil.TempDir(t)
	b := NewBashTool(dir, alwaysApprove)
	var asked bool
	b.SetSudoPasswordFn(func(ctx context.Context, command, description, workdir string) ([]byte, bool) {
		asked = true
		return []byte("hunter2"), true
	})

	command := "cat <<'EOF'\nsudo apt update\nEOF"
	res, err := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command": command, "description": "heredoc body mentioning sudo, never executed as a command",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if asked {
		t.Error("sudoPasswordFn was called for a heredoc body that merely mentions sudo as text, never executes it")
	}
}
