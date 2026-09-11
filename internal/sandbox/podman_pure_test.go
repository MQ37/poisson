package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestFullArgs_NilGlobalArgs: no globalArgs configured (the production
// default) — fullArgs must pass the subcommand args through unchanged, not
// e.g. prepend a stray empty element.
func TestFullArgs_NilGlobalArgs(t *testing.T) {
	d := &podmanDriver{}
	got := d.fullArgs("ps", "-a")
	want := []string{"ps", "-a"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fullArgs = %v, want %v", got, want)
	}
}

// TestFullArgs_GlobalArgsPrepended: globalArgs (e.g. --root/--runroot, used
// by the gated integration suite to confine storage to a disposable /tmp
// root) must come before the subcommand args, not after or interleaved.
func TestFullArgs_GlobalArgsPrepended(t *testing.T) {
	d := &podmanDriver{globalArgs: []string{"--root", "/tmp/x", "--runroot", "/tmp/y"}}
	got := d.fullArgs("ps", "-a", "--filter", "label=poisson.sandbox=1")
	want := []string{"--root", "/tmp/x", "--runroot", "/tmp/y", "ps", "-a", "--filter", "label=poisson.sandbox=1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fullArgs = %v, want %v", got, want)
	}
}

// TestEnv_NilWhenNoExtraEnv: os/exec treats a nil Cmd.Env as "inherit the
// parent environment" and an empty-but-non-nil slice as "run with NO
// environment at all" — very different outcomes. With no extraEnv
// configured (the production default), env() must return exactly nil, not
// merely an empty slice.
func TestEnv_NilWhenNoExtraEnv(t *testing.T) {
	d := &podmanDriver{}
	got := d.env()
	if got != nil {
		t.Fatalf("env() = %#v (len %d), want nil", got, len(got))
	}
}

// TestEnv_AppendsToOSEnviron: with extraEnv set (the integration suite's
// XDG_*/TMPDIR overrides), env() must be os.Environ() PLUS extraEnv
// appended — the inherited environment must still be present, not
// replaced. Uses t.Setenv on a unique marker so the assertion doesn't rely
// on incidental unrelated env vars.
func TestEnv_AppendsToOSEnviron(t *testing.T) {
	t.Setenv("POISSON_PODMAN_ENV_TEST_MARKER", "marker-9f3a")

	d := &podmanDriver{extraEnv: []string{"XDG_DATA_HOME=/tmp/fake-data"}}
	got := d.env()

	var sawMarker, sawExtra bool
	for _, e := range got {
		if e == "POISSON_PODMAN_ENV_TEST_MARKER=marker-9f3a" {
			sawMarker = true
		}
		if e == "XDG_DATA_HOME=/tmp/fake-data" {
			sawExtra = true
		}
	}
	if !sawMarker {
		t.Errorf("env() missing inherited marker var; got %v", got)
	}
	if !sawExtra {
		t.Errorf("env() missing extraEnv entry; got %v", got)
	}
	// extraEnv is appended, not prepended: it must be the tail of the slice.
	if len(got) == 0 || got[len(got)-1] != "XDG_DATA_HOME=/tmp/fake-data" {
		t.Errorf("env() tail = %v, want extraEnv appended at the end", got)
	}
}

// TestBootstrapScript_SubstitutesUidGid confirms the uid/gid substitution:
// the getent/groupadd/useradd lines must carry the exact numbers passed in,
// at the right positions (uid appears where gid shouldn't, and vice versa).
func TestBootstrapScript_SubstitutesUidGid(t *testing.T) {
	script := bootstrapScript(1000, 1000)

	for _, want := range []string{
		"getent passwd 1000 >/dev/null",
		"getent group 1000 >/dev/null || groupadd -g 1000 poisson",
		"useradd -u 1000 -g 1000 -m -s /bin/bash poisson",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("bootstrapScript(1000, 1000) missing %q\nfull script:\n%s", want, script)
		}
	}
}

// TestBootstrapScript_DistinctUidGid: uid and gid substituted independently
// (not the same value silently reused) — catches an fmt.Sprintf argument
// mix-up.
func TestBootstrapScript_DistinctUidGid(t *testing.T) {
	script := bootstrapScript(1500, 2000)

	for _, want := range []string{
		"getent passwd 1500 >/dev/null",
		"U=$(getent passwd 1500 | cut -d: -f1)",
		"getent group 2000 >/dev/null || groupadd -g 2000 poisson",
		"useradd -u 1500 -g 2000 -m -s /bin/bash poisson",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("bootstrapScript(1500, 2000) missing %q\nfull script:\n%s", want, script)
		}
	}
	if strings.Contains(script, "1500") && strings.Contains(script, "useradd -u 2000") {
		t.Errorf("uid/gid appear swapped in useradd line:\n%s", script)
	}
}

// TestBootstrapScript_SudoersNopasswd pins the passwordless-sudo grant —
// the whole point of bootstrap per docs/sandbox-plan.md's "Root access"
// section.
func TestBootstrapScript_SudoersNopasswd(t *testing.T) {
	script := bootstrapScript(1000, 1000)

	if !strings.Contains(script, `echo "$U ALL=(ALL) NOPASSWD:ALL" >/etc/sudoers.d/poisson-sandbox`) {
		t.Errorf("bootstrapScript missing NOPASSWD sudoers line:\n%s", script)
	}
	if !strings.Contains(script, "chmod 0440 /etc/sudoers.d/poisson-sandbox") {
		t.Errorf("bootstrapScript missing sudoers file permission fix:\n%s", script)
	}
}

// TestBootstrapScript_ControlFlowMarkers confirms the idempotent
// "reuse existing user at that uid, else create one" branch structure
// described in bootstrapScript's doc comment.
func TestBootstrapScript_ControlFlowMarkers(t *testing.T) {
	script := bootstrapScript(1000, 1000)

	for _, want := range []string{
		"set -e",
		"if getent passwd 1000 >/dev/null; then",
		"else",
		"fi",
		"export DEBIAN_FRONTEND=noninteractive",
		"command -v sudo >/dev/null 2>&1 || (apt-get update -qq && apt-get install -y -qq sudo >/dev/null 2>&1)",
		`printf '%s' "$U"`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("bootstrapScript missing control-flow marker %q\nfull script:\n%s", want, script)
		}
	}
}

// TestBootstrap_TimeoutProducesActionableError is a regression test for the
// real incident this covers: bootstrap hanging on apt-get update (no
// container network) used to surface as bare "signal: killed" — meaningless
// to both the agent and the user. Fakes podmanBin as a script that just
// sleeps past a shrunk bootstrapTimeout, so exec.CommandContext SIGKILLs it
// exactly like the real hang did, and checks the error names the actual
// cause instead.
func TestBootstrap_TimeoutProducesActionableError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake podman script assumes a POSIX shell")
	}
	fake := filepath.Join(t.TempDir(), "podman")
	// "exec sleep" (not a bare "sleep" line) replaces the shell process
	// in-place instead of forking a child: cmd.Wait() waits on stdout/
	// stderr pipe EOF as well as process exit, and a forked grandchild
	// would keep those pipes open — and the test hanging for the full
	// sleep duration — even after SIGKILL reaps the direct child shell.
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexec sleep 5\n"), 0o755); err != nil {
		t.Fatalf("write fake podman: %v", err)
	}

	origBin, origTimeout := podmanBin, bootstrapTimeout
	podmanBin = fake
	bootstrapTimeout = 50 * time.Millisecond
	t.Cleanup(func() { podmanBin = origBin; bootstrapTimeout = origTimeout })

	d := &podmanDriver{execUser: make(map[string]string)}
	_, err := d.bootstrap(context.Background(), "fake-container-id")
	if err == nil {
		t.Fatal("expected an error from a bootstrap that never returns")
	}
	if strings.Contains(err.Error(), "signal: killed") {
		t.Fatalf("error still leaks the raw signal instead of naming the cause: %v", err)
	}
	if !strings.Contains(err.Error(), "timed out after 50ms") || !strings.Contains(err.Error(), "network") {
		t.Fatalf("error = %v, want it to name the timeout and the network cause", err)
	}
}

// TestBootstrapScript_BashSyntaxCheck: extra rigor beyond string-contains —
// if bash is on PATH, pipe the generated script through `bash -n` to catch
// a syntax error (e.g. an unbalanced if/fi) that substring assertions alone
// wouldn't. Skips (not fails) when bash isn't available, since a real shell
// isn't required to validate this function.
func TestBootstrapScript_BashSyntaxCheck(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	script := bootstrapScript(1000, 1000)
	cmd := exec.Command("bash", "-n")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bash -n rejected generated script: %v\n%s\nscript:\n%s", err, out, script)
	}
}
