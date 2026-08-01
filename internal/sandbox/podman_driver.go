package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// podmanBin is the podman binary. A package var (not a hardcoded literal),
// same idiom as internal/tools/grep.go's rgBin — overridable in tests, even
// though the gated integration suite wants the real one.
var podmanBin = "podman"

// bootstrapTimeout bounds Create's one-time bootstrap exec (matching-uid
// user creation + a possible `apt-get install sudo`, which needs a package-
// manager network hit the first time — see docs/sandbox-plan.md's "Root
// access" section).
const bootstrapTimeout = 90 * time.Second

// podmanDriver shells out to the real podman CLI — no REST API, no new
// dependency, same style as internal/tools/bash.go and grep.go.
type podmanDriver struct {
	// globalArgs is an optional prefix inserted before every subcommand
	// (e.g. ["--root", X, "--runroot", Y]) — nil in production (real
	// create_sandbox uses whatever storage the host user has configured);
	// only the gated integration suite sets it, to confine all storage to a
	// disposable /tmp root.
	globalArgs []string
	// extraEnv is appended to os.Environ() for every invocation — nil in
	// production; the integration suite uses it for the matching
	// XDG_DATA_HOME/XDG_CONFIG_HOME/XDG_RUNTIME_DIR/TMPDIR overrides that
	// close the gaps globalArgs's --root/--runroot alone don't cover.
	extraEnv []string

	mu       sync.Mutex
	execUser map[string]string // container id -> resolved exec-as user, set once Create's bootstrap succeeds
}

// NewPodmanDriver returns a Driver backed by the real podman CLI. globalArgs
// and extraEnv are nil for normal production use.
func NewPodmanDriver(globalArgs, extraEnv []string) Driver {
	return &podmanDriver{globalArgs: globalArgs, extraEnv: extraEnv, execUser: make(map[string]string)}
}

func (d *podmanDriver) fullArgs(sub ...string) []string {
	return append(append([]string{}, d.globalArgs...), sub...)
}

func (d *podmanDriver) env() []string {
	if len(d.extraEnv) == 0 {
		return nil // nil Cmd.Env means "inherit the process environment" — see os/exec
	}
	return append(os.Environ(), d.extraEnv...)
}

// run executes one podman invocation to completion, capturing stdout/stderr.
// Used for everything except the long-running Exec call, which needs its
// own timeout/cancellation handling.
func (d *podmanDriver) run(ctx context.Context, args ...string) (stdout, stderr string, err error) {
	cmd := exec.CommandContext(ctx, podmanBin, d.fullArgs(args...)...)
	cmd.Env = d.env()
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return out.String(), errBuf.String(), err
}

// createArgs builds the `podman create` argument list for a container named
// name per opts. Split out from Create so it's unit-testable without a real
// podman binary.
func createArgs(name string, opts CreateOpts) []string {
	// --userns=keep-id: without it, a uid that matches on both sides of the
	// bind mount is a coincidence, not a guarantee — rootless podman's
	// default user-namespace mapping means "uid 1000 inside the container"
	// and "uid 1000 on the host" are different real identities unless
	// keep-id aligns them. Confirmed empirically: bootstrapping a user at
	// the host's numeric uid/gid without keep-id produced a container user
	// that could not actually write to the bind-mounted workspace.
	//
	// --label poisson.sandbox=1 marks every container this package creates
	// so List can filter podman's own container list down to just these —
	// the mechanism a fresh process uses to rediscover a sandbox after a
	// crash, or from a different session entirely. poisson.session is
	// purely informational (list_sandboxes), added only when non-empty.
	//
	// --init: run podman's built-in catatonit as PID 1 instead of the raw
	// `sleep infinity` below. Without it, PID 1 never calls wait() on
	// anything, so any process a bash tool call or subagent backgrounds
	// (or leaves running when killed mid-command) reparents to PID 1 on
	// exit and stays a zombie for the container's entire lifetime —
	// sandboxes are designed to be long-lived and crash-surviving (see
	// docs/sandbox-plan.md), so that's unbounded, not a one-time leak.
	// catatonit reaps orphans the normal init way and still execs straight
	// into `sleep infinity` as its own child, so nothing else here changes.
	args := []string{"create", "--init", "--userns=keep-id", "--name", name, "--label", "poisson.sandbox=1"}
	if opts.SessionID != "" {
		args = append(args, "--label", "poisson.session="+opts.SessionID)
	}
	// --memory: a shared-kernel resource limit (20% of host total, see
	// memlimit.go) — a sandbox with no cap can otherwise OOM the whole host,
	// not just itself. Omitted, not a hard error, when total memory can't be
	// determined (e.g. no /proc/meminfo).
	if limit, ok := sandboxMemoryLimit(); ok {
		args = append(args, "--memory", limit)
	}
	// nosuid,nodev on every bind mount: defense-in-depth against a planted
	// setuid binary or device node in the mounted tree mattering if it's ever
	// executed/opened outside the sandbox (see docs/sandbox-plan.md's "Mount
	// safety" section).
	if opts.HostPath != "" {
		args = append(args, "-v", opts.HostPath+":/workspace:Z,nosuid,nodev")
	}
	for _, m := range opts.Mounts {
		mode := "rw"
		if m.ReadOnly {
			mode = "ro"
		}
		args = append(args, "-v", fmt.Sprintf("%s:%s:%s,nosuid,nodev", m.HostPath, m.ContainerPath, mode))
	}
	for _, e := range opts.Env {
		args = append(args, "-e", e)
	}
	return append(args, opts.Image, "sleep", "infinity")
}

// Create starts a new container per opts: pinned image, opts.HostPath
// bind-mounted as /workspace, any extra opts.Mounts/opts.Env, then runs the
// one-time bootstrap (see bootstrapScript) before returning. Cleans up the
// container itself on any failure after it was created — the caller (e.g.
// CreateSandboxTool) is responsible for cleaning up anything it made on the
// host side (its scratch workspace directory) when Create returns an error.
func (d *podmanDriver) Create(ctx context.Context, opts CreateOpts) (string, error) {
	if strings.TrimSpace(opts.Image) == "" {
		return "", fmt.Errorf("podman create: image is required")
	}

	// name is both the container's actual --name and the id this Create
	// returns — a sandbox's name *is* its id, no separate opaque handle (see
	// CreateOpts.Name / docs/sandbox-plan.md's "Crash recovery" section).
	name, err := ResolveSandboxName(opts.Name)
	if err != nil {
		return "", err
	}

	args := createArgs(name, opts)

	if _, stderr, err := d.run(ctx, args...); err != nil {
		return "", fmt.Errorf("podman create: %w (%s)", err, strings.TrimSpace(stderr))
	}

	if _, stderr, err := d.run(ctx, "start", name); err != nil {
		d.forceRemove(context.Background(), name)
		return "", fmt.Errorf("podman start: %w (%s)", err, strings.TrimSpace(stderr))
	}

	user, err := d.bootstrap(ctx, name)
	if err != nil {
		d.forceRemove(context.Background(), name)
		return "", fmt.Errorf("bootstrap: %w", err)
	}
	d.mu.Lock()
	d.execUser[name] = user
	d.mu.Unlock()

	return name, nil
}

// bootstrapScript creates (or reuses an existing user at) the host's own
// uid/gid inside the container, and grants it passwordless sudo — see
// docs/sandbox-plan.md's "Root access" section. Prints the resolved
// username as its only stdout output.
//
// Always checks for an existing user at the target uid first, rather than
// assuming a fresh user gets created: `ubuntu:26.04` (and most other cloud-
// oriented base images) ship a pre-existing uid-1000 "ubuntu" user, and uid
// 1000 is the near-universal default for the first regular user on Linux —
// a naive useradd collides with it far more often than not. Confirmed
// empirically against the real image.
func bootstrapScript(uid, gid int) string {
	return fmt.Sprintf(`set -e
if getent passwd %d >/dev/null; then
  U=$(getent passwd %d | cut -d: -f1)
else
  getent group %d >/dev/null || groupadd -g %d poisson
  useradd -u %d -g %d -m -s /bin/bash poisson
  U=poisson
fi
export DEBIAN_FRONTEND=noninteractive
command -v sudo >/dev/null 2>&1 || (apt-get update -qq && apt-get install -y -qq sudo >/dev/null 2>&1)
echo "$U ALL=(ALL) NOPASSWD:ALL" >/etc/sudoers.d/poisson-sandbox
chmod 0440 /etc/sudoers.d/poisson-sandbox
printf '%%s' "$U"
`, uid, uid, gid, gid, uid, gid)
}

// bootstrap runs bootstrapScript inside container id and returns the
// resolved username to exec future ordinary commands as. Explicitly
// `--user root`: with --userns=keep-id (set by Create), a plain podman exec
// with no --user defaults to the keep-id-mapped identity, not root —
// confirmed empirically (bootstrap's own apt-get/useradd otherwise fail
// with permission errors) — root is exactly what bootstrap needs and
// nothing else in this driver assumes as a default.
func (d *podmanDriver) bootstrap(ctx context.Context, id string) (user string, err error) {
	bctx, cancel := context.WithTimeout(ctx, bootstrapTimeout)
	defer cancel()
	stdout, stderr, err := d.run(bctx, "exec", "--user", "root", id, "bash", "-c", bootstrapScript(os.Getuid(), os.Getgid()))
	if err != nil {
		return "", fmt.Errorf("%w (%s)", err, strings.TrimSpace(stderr))
	}
	user = strings.TrimSpace(stdout)
	if user == "" {
		return "", fmt.Errorf("bootstrap produced no resolved username")
	}
	return user, nil
}

// Exec runs cmd inside container id as the user Create's bootstrap
// resolved (never root by default — sudo is the only path to root, per the
// design), in workdir (defaulting to /workspace when empty, since that's
// the one directory an ordinary sandboxed command actually wants to start
// in), bounded by timeout.
//
// Known limitation: killing the local `podman exec` client process on
// timeout (via ctx) stops poisson from hanging forever, but does not
// guarantee the process running inside the container's own pid namespace
// actually dies — it isn't a child of this local client the way a host
// bash subprocess is. A full fix needs more machinery (tracking the remote
// pid, a second targeted exec to kill it) — accepted as a v1 gap, same
// "ship the naive version first" principle as the no-warm-process decision
// in docs/sandbox-plan.md's "Non-goals".
func (d *podmanDriver) Exec(ctx context.Context, id, cmd, workdir string, timeout time.Duration) (stdout, stderr string, exitCode int, err error) {
	d.mu.Lock()
	user := d.execUser[id]
	d.mu.Unlock()

	wd := workdir
	if wd == "" {
		wd = "/workspace"
	}
	args := []string{"exec"}
	if user != "" {
		args = append(args, "--user", user)
	}
	args = append(args, "--workdir", wd, id, "bash", "-c", cmd)

	childCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	c := exec.CommandContext(childCtx, podmanBin, d.fullArgs(args...)...)
	c.Env = d.env()
	var outBuf, errBuf bytes.Buffer
	c.Stdout = &outBuf
	c.Stderr = &errBuf
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error {
		if c.Process == nil {
			return nil
		}
		return syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
	}
	c.WaitDelay = 2 * time.Second

	runErr := c.Run()
	exitCode = 0
	if runErr != nil {
		var exitErr *exec.ExitError
		switch {
		case errors.As(runErr, &exitErr):
			exitCode = exitErr.ExitCode()
		case errors.Is(runErr, exec.ErrWaitDelay):
			// The command itself exited; only a child it left running past
			// its own exit still held the pipe open (same case bash.go's
			// host path documents for exec.ErrWaitDelay).
		default:
			return "", "", -1, fmt.Errorf("podman exec: %w (%s)", runErr, strings.TrimSpace(errBuf.String()))
		}
	}
	return outBuf.String(), errBuf.String(), exitCode, nil
}

// Inspect reports whether container id is still running. podman inspect
// exits non-zero when the container doesn't exist at all — that's "not
// alive", not an error worth propagating.
func (d *podmanDriver) Inspect(ctx context.Context, id string) (bool, error) {
	stdout, _, err := d.run(ctx, "inspect", "--format", "{{.State.Running}}", id)
	if err != nil {
		return false, nil
	}
	return strings.TrimSpace(stdout) == "true", nil
}

// Kill stops and removes container id. -t 0 skips the SIGTERM grace period
// (`sleep infinity` doesn't handle SIGTERM, so without this every teardown
// pays a ~10s wait before podman escalates to SIGKILL — confirmed
// empirically).
func (d *podmanDriver) Kill(ctx context.Context, id string) error {
	d.mu.Lock()
	delete(d.execUser, id)
	d.mu.Unlock()
	return d.forceRemove(ctx, id)
}

func (d *podmanDriver) forceRemove(ctx context.Context, id string) error {
	_, stderr, err := d.run(ctx, "rm", "-f", "-t", "0", id)
	if err != nil {
		return fmt.Errorf("podman rm: %w (%s)", err, strings.TrimSpace(stderr))
	}
	return nil
}

// List reports every container carrying the poisson.sandbox label,
// regardless of which process created it — the mechanism behind cross-
// session discovery and crash recovery (see Manager.EnableDiscovery). Two
// podman calls: an id-only `ps -a` first (so an empty result short-circuits
// before any inspect call), then one batched `inspect` for full detail
// (name, labels, running state, the /workspace bind-mount source).
func (d *podmanDriver) List(ctx context.Context) ([]Info, error) {
	stdout, stderr, err := d.run(ctx, "ps", "-a", "--filter", "label=poisson.sandbox=1", "--format", "{{.ID}}")
	if err != nil {
		return nil, fmt.Errorf("podman ps: %w (%s)", err, strings.TrimSpace(stderr))
	}
	ids := strings.Fields(stdout)
	if len(ids) == 0 {
		return nil, nil
	}

	stdout, stderr, err = d.run(ctx, append([]string{"inspect"}, ids...)...)
	if err != nil {
		return nil, fmt.Errorf("podman inspect: %w (%s)", err, strings.TrimSpace(stderr))
	}
	var raw []struct {
		Name    string
		Created string
		State   struct{ Running bool }
		Config  struct{ Labels map[string]string }
		Mounts  []struct{ Destination, Source string }
	}
	if err := json.Unmarshal([]byte(stdout), &raw); err != nil {
		return nil, fmt.Errorf("parse podman inspect: %w", err)
	}

	infos := make([]Info, 0, len(raw))
	for _, c := range raw {
		info := Info{
			ID:        strings.TrimPrefix(c.Name, "/"),
			Running:   c.State.Running,
			SessionID: c.Config.Labels["poisson.session"],
		}
		if t, err := time.Parse(time.RFC3339Nano, c.Created); err == nil {
			info.CreatedAt = t
		}
		for _, m := range c.Mounts {
			if m.Destination == "/workspace" {
				info.HostPath = m.Source
			}
		}
		infos = append(infos, info)
	}
	return infos, nil
}
