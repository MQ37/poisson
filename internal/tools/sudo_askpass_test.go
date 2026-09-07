package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/testutil"
)

func TestInjectSudoAskpass(t *testing.T) {
	cases := []struct {
		command string
		want    string
	}{
		{"sudo whoami", "sudo -A -k whoami"},
		{"sudo -u root whoami", "sudo -A -k -u root whoami"},
		{"FOO=bar sudo whoami", "FOO=bar sudo -A -k whoami"},
		{"echo hi | sudo tee /etc/foo", "echo hi | sudo -A -k tee /etc/foo"},
		{"ls -la && sudo reboot", "ls -la && sudo -A -k reboot"},
		{"pkexec ls /root", "pkexec ls /root"}, // not rewritten — see guard.RequiresSudoPassword
		{"ls -la", "ls -la"},                   // no sudo — untouched
	}
	for _, c := range cases {
		if got := injectSudoAskpass(c.command); got != c.want {
			t.Errorf("injectSudoAskpass(%q) = %q, want %q", c.command, got, c.want)
		}
	}
}

func TestSudoAskpassHelperWritesAndCleansUp(t *testing.T) {
	password := []byte("hunter2")
	h, err := newSudoAskpassHelper(password)
	if err != nil {
		t.Fatalf("newSudoAskpassHelper: %v", err)
	}

	// Directory and password file are private to the invoking user.
	dirInfo, err := os.Stat(h.dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("askpass dir perm = %o, want 0700", perm)
	}
	pwInfo, err := os.Stat(h.pwPath)
	if err != nil {
		t.Fatalf("stat pw file: %v", err)
	}
	if perm := pwInfo.Mode().Perm(); perm != 0o600 {
		t.Errorf("askpass pw file perm = %o, want 0600", perm)
	}
	got, err := os.ReadFile(h.pwPath)
	if err != nil {
		t.Fatalf("read pw file: %v", err)
	}
	if string(got) != "hunter2" {
		t.Errorf("pw file content = %q, want %q", got, "hunter2")
	}
	if base := filepath.Base(h.pwPath); base == "pw" || strings.Contains(base, "password") {
		t.Errorf("pw file uses an obviously secret-shaped name %q, want a random one", base)
	}
	script, err := os.ReadFile(h.scriptPath)
	if err != nil {
		t.Fatalf("read script: %v", err)
	}
	if strings.Contains(string(script), "hunter2") {
		t.Errorf("askpass script embeds the password directly: %s", script)
	}
	if !strings.Contains(string(script), h.pwPath) {
		t.Errorf("askpass script doesn't reference its pw file: %s", script)
	}

	h.cleanup()
	if _, err := os.Stat(h.dir); !os.IsNotExist(err) {
		t.Errorf("askpass dir still exists after cleanup: err=%v", err)
	}
}

// TestSudoPasswordSurvivesNaivePatternScan documents the accepted residual
// risk on record (see sudoAskpassHelper's doc comment): the password file
// is same-uid-readable for the whole command's runtime, so a background
// job the SAME approved command spawns could in principle race to read it.
// An anonymous-pipe design (fd 3, no disk footprint at all) was tried and
// reverted — it broke real sudo, which sanitizes file descriptors before
// exec'ing its own askpass helper on at least some builds, closing fd 3
// before the script ever runs. This asserts the mitigation that IS in
// place: a random filename (not "pw"/"password*") defeats a scan for an
// obvious secret-shaped name, same as the one that found the original bug.
// It is not a claim the risk is eliminated — see the doc comment.
func TestSudoPasswordSurvivesNaivePatternScan(t *testing.T) {
	fakeDir := t.TempDir()
	fakeSudo := filepath.Join(fakeDir, "sudo")
	script := `#!/bin/sh
if [ "$1" != "-A" ] || [ "$2" != "-k" ]; then
	echo "fake sudo: expected -A -k, got: $*" >&2
	exit 1
fi
shift 2
sleep 0.2
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
	b.SetSudoPasswordFn(func(ctx context.Context, command, description, workdir string) ([]byte, bool) {
		return []byte("hunter2"), true
	})

	// A background job races a naive pattern-based scan against the fake
	// sudo's own 0.2s delay, while sudo itself still authenticates fine.
	res, err := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command":     `(find /tmp -maxdepth 2 -iname 'pw' -o -iname 'password*' 2>/dev/null | xargs -r cat 2>/dev/null) & sudo whoami`,
		"description": "scan probe",
	}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	var out bashOutput
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if strings.Contains(out.Stdout, "hunter2") || strings.Contains(out.Stderr, "hunter2") {
		t.Fatalf("password leaked via naive pattern scan: stdout=%q stderr=%q", out.Stdout, out.Stderr)
	}
}

// TestBashToolSudoRejectsWrongPasswordEvenAfterACorrectOne is a regression
// test for the actual bug reported in real usage: a fake sudo simulating
// real sudo's own credential-ticket cache (valid until a matching -k
// resets it) proves a WRONG password on a SECOND, separate BashTool.Execute
// call is genuinely rejected — not silently waved through on a ticket
// primed by the first call's correct one. Without -k in the rewritten
// command, this fake (and real sudo, confirmed by hand) would let the
// second call through regardless of what password it got.
func TestBashToolSudoRejectsWrongPasswordEvenAfterACorrectOne(t *testing.T) {
	fakeDir := t.TempDir()
	ticketPath := filepath.Join(fakeDir, "ticket")
	fakeSudo := filepath.Join(fakeDir, "sudo")
	script := `#!/bin/sh
ticket="` + ticketPath + `"
if [ "$1" != "-A" ]; then
	echo "fake sudo: expected -A" >&2
	exit 1
fi
shift
if [ "$1" = "-k" ]; then
	rm -f "$ticket"
	shift
fi
if [ -f "$ticket" ]; then
	exec "$@"
fi
pw=$("$SUDO_ASKPASS")
if [ "$pw" != "correct" ]; then
	echo "fake sudo: Authentication failed" >&2
	exit 1
fi
touch "$ticket"
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

	var supplied string
	b.SetSudoPasswordFn(func(ctx context.Context, command, description, workdir string) ([]byte, bool) {
		return []byte(supplied), true
	})

	supplied = "correct"
	res1, err := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command": "sudo whoami", "description": "first call, correct password",
	}))
	if err != nil {
		t.Fatalf("Execute (first): %v", err)
	}
	if res1.Error != "" {
		t.Fatalf("first call should succeed: %s", res1.Error)
	}
	var out1 bashOutput
	if err := json.Unmarshal([]byte(res1.Content), &out1); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out1.ExitCode != 0 {
		t.Fatalf("first call (correct password) should succeed: exitCode=%d stdout=%q stderr=%q", out1.ExitCode, out1.Stdout, out1.Stderr)
	}

	supplied = "wrong"
	res2, err := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command": "sudo whoami", "description": "second call, wrong password",
	}))
	if err != nil {
		t.Fatalf("Execute (second): %v", err)
	}
	var out2 bashOutput
	if err := json.Unmarshal([]byte(res2.Content), &out2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out2.ExitCode == 0 {
		t.Fatalf("second call with a WRONG password succeeded — ticket cache bypassed the check (stdout=%q stderr=%q)", out2.Stdout, out2.Stderr)
	}
}

// TestBashToolSudoNoPromptWiredFails covers the host path with no
// SudoPasswordFn set (headless default): a sudo-needing command must fail
// clearly, never hang and never run without authentication.
func TestBashToolSudoNoPromptWiredFails(t *testing.T) {
	dir := testutil.TempDir(t)
	b := NewBashTool(dir, alwaysApprove)
	out, err := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command": "sudo whoami", "description": "test",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.Error == "" || !strings.Contains(out.Error, "sudo password") {
		t.Errorf("Error = %q, want a sudo-password-unavailable message", out.Error)
	}
}

// TestBashToolSudoPasswordCancelled covers a human cancelling the password
// prompt: denied like any other tool call, no exec attempted.
func TestBashToolSudoPasswordCancelled(t *testing.T) {
	dir := testutil.TempDir(t)
	b := NewBashTool(dir, alwaysApprove)
	b.SetSudoPasswordFn(func(ctx context.Context, command, description, workdir string) ([]byte, bool) {
		return nil, false
	})
	out, err := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command": "sudo whoami", "description": "test",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out.Error, "cancelled") {
		t.Errorf("Error = %q, want cancellation message", out.Error)
	}
}

// TestBashToolSudoPasswordFlowEndToEnd fakes the sudo binary itself (no real
// root needed) to prove the full wiring: the command is rewritten with
// "-A -k" (the fake errors otherwise — -k is what makes sudo actually
// re-check the password every call instead of reusing a cached ticket from
// an earlier one, see injectSudoAskpass's own doc comment), SUDO_ASKPASS is
// set to the one-shot helper script, the fake sudo reads the password
// through it, and the password never appears in the command's own
// stdout/stderr even when the command dumps its whole environment.
func TestBashToolSudoPasswordFlowEndToEnd(t *testing.T) {
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
	var gotCommand string
	b.SetSudoPasswordFn(func(ctx context.Context, command, description, workdir string) ([]byte, bool) {
		gotCommand = command
		return []byte("hunter2"), true
	})

	res, err := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command":     "sudo whoami && env",
		"description": "test",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotCommand != "sudo whoami && env" {
		t.Errorf("sudoPasswordFn saw command = %q, want original unrewritten command", gotCommand)
	}
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	var out bashOutput
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if strings.Contains(out.Stdout, "hunter2") || strings.Contains(out.Stderr, "hunter2") {
		t.Errorf("password leaked into tool output: stdout=%q stderr=%q", out.Stdout, out.Stderr)
	}
	if !strings.Contains(out.Stdout, "SUDO_ASKPASS=") {
		t.Fatalf("expected SUDO_ASKPASS to show up in env dump (path only), stdout=%q", out.Stdout)
	}
}
