package nspawn

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mq37/poisson/internal/auth"
	"github.com/mq37/poisson/internal/orchestrator"
	"github.com/mq37/poisson/internal/subagent"
)

// Binaries this package shells out to — package vars (not hardcoded
// literals), same idiom as sandbox.podmanBin, so a future test double can
// override them.
var (
	machinectlBin = "machinectl"
	systemctlBin  = "systemctl"
	systemdRunBin = "systemd-run"
	journalctlBin = "journalctl"
)

// startReadinessTimeout bounds how long Start waits for an instance to
// actually finish booting — `systemctl start` itself returns before boot
// completes, so a real readiness probe (not just that return) is required.
const startReadinessTimeout = 30 * time.Second

// minFreeSpaceMultiplier: Create refuses to clone the golden image unless
// free space under MachinesRoot is at least this many times the golden
// image's own size — orchestrator-host's root is ext4, so every clone is a full
// recursive copy, not a CoW snapshot (see docs/orchestrator-plan.md Step 12).
const minFreeSpaceMultiplier = 3

// Runtime implements orchestrator.Runtime by shelling out to
// systemd-nspawn/machinectl/systemd-run. The only production implementation
// of orchestrator.Runtime — see FakeRuntime (M3) for the in-memory test
// double Core is built and tested against.
type Runtime struct {
	cfg Config
}

// NewRuntime returns a Runtime for cfg.
func NewRuntime(cfg Config) *Runtime {
	return &Runtime{cfg: cfg}
}

var _ orchestrator.Runtime = (*Runtime)(nil)

// run executes one host command to completion, capturing stdout/stderr.
func (r *Runtime) run(ctx context.Context, bin string, args ...string) (stdout, stderr string, err error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return out.String(), errBuf.String(), err
}

// ensureTemplateUnit idempotently installs the shared px-instance@.service
// template — a no-op (no daemon-reload) if the file already exists with
// exactly this content, so repeated Create calls don't churn systemd's unit
// cache for no reason.
func (r *Runtime) ensureTemplateUnit(ctx context.Context) error {
	want := templateUnitContent(r.cfg)
	existing, err := os.ReadFile(templateUnitPath)
	if err == nil && string(existing) == want {
		return nil
	}
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read template unit: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(templateUnitPath), 0o755); err != nil {
		return fmt.Errorf("create systemd unit dir: %w", err)
	}
	if err := os.WriteFile(templateUnitPath, []byte(want), 0o644); err != nil {
		return fmt.Errorf("write template unit: %w", err)
	}
	if _, stderr, err := r.run(ctx, systemctlBin, systemctlDaemonReloadArgs()...); err != nil {
		return fmt.Errorf("daemon-reload: %w (%s)", err, strings.TrimSpace(stderr))
	}
	return nil
}

// Create clones the golden rootfs into a brand-new instance, generates its
// secrets/config, and installs its resource-limit drop-in. Does not start
// it. Cleans up everything it created on any failure after the clone
// succeeds, rather than leaving a half-created instance behind (mirrors
// podmanDriver.Create's own cleanup-on-failure contract).
func (r *Runtime) Create(ctx context.Context, spec orchestrator.InstanceSpec) error {
	if err := orchestrator.ValidateInstanceName(spec.Name); err != nil {
		return err
	}
	if err := r.ensureTemplateUnit(ctx); err != nil {
		return err
	}

	goldenPath := rootfsPath(r.cfg, r.cfg.GoldenImage)
	goldenSize, err := dirSize(goldenPath)
	if err != nil {
		return fmt.Errorf("measure golden image size: %w", err)
	}
	free, err := freeBytes(r.cfg.MachinesRoot)
	if err != nil {
		return fmt.Errorf("check free space: %w", err)
	}
	if free < goldenSize*minFreeSpaceMultiplier {
		return fmt.Errorf("insufficient free space under %s: have %d bytes, want at least %dx the golden image's %d bytes",
			r.cfg.MachinesRoot, free, minFreeSpaceMultiplier, goldenSize)
	}

	rfPath := rootfsPath(r.cfg, spec.Name)
	if _, err := os.Stat(rfPath); err == nil {
		return fmt.Errorf("rootfs already exists at %s", rfPath)
	}

	if _, stderr, err := r.run(ctx, machinectlBin, cloneArgs(r.cfg.GoldenImage, spec.Name)...); err != nil {
		return fmt.Errorf("machinectl clone: %w (%s)", err, strings.TrimSpace(stderr))
	}

	// Best-effort teardown of everything Create may have already made —
	// called on every failure path below the clone.
	cleanup := func() {
		r.run(context.Background(), machinectlBin, machinectlRemoveArgs(spec.Name)...)
		os.RemoveAll(instanceStateDir(r.cfg, spec.Name))
		os.RemoveAll(dropInDir(spec.Name))
	}

	if err := orchestrator.CreateInstanceLayout(r.cfg.StateRoot, spec.Name); err != nil {
		cleanup()
		return fmt.Errorf("create instance layout: %w", err)
	}

	// Loaded here, not passed in by the caller: Create is the one place
	// credentials are copied into an instance, so this is where the host's
	// real auth store is read — see GenerateInstanceSecrets's own doc
	// comment on why only spec.AuthorizedProviders' entries survive.
	hostAuth, err := auth.Load()
	if err != nil {
		cleanup()
		return fmt.Errorf("load host auth store: %w", err)
	}
	if err := orchestrator.GenerateInstanceSecrets(r.cfg.StateRoot, spec, hostAuth); err != nil {
		cleanup()
		return fmt.Errorf("generate instance secrets: %w", err)
	}

	if err := os.MkdirAll(dropInDir(spec.Name), 0o755); err != nil {
		cleanup()
		return fmt.Errorf("create drop-in dir: %w", err)
	}
	if err := os.WriteFile(dropInPath(spec.Name), []byte(instanceDropInContent(r.cfg)), 0o644); err != nil {
		cleanup()
		return fmt.Errorf("write drop-in: %w", err)
	}
	if _, stderr, err := r.run(ctx, systemctlBin, systemctlDaemonReloadArgs()...); err != nil {
		cleanup()
		return fmt.Errorf("daemon-reload: %w (%s)", err, strings.TrimSpace(stderr))
	}
	return nil
}

// Start boots instance name and polls for real readiness — `systemctl
// start` itself returns once the unit is queued, well before nspawn's
// --boot actually finishes bringing up the guest's own init. On timeout,
// the recent journal is captured into the returned error so a boot failure
// is diagnosable rather than just "timed out" (this is also where a
// MemoryMax-triggered OOM kill on orchestrator-host's swapless setup would surface).
// Idempotent: starting an already-running instance just succeeds, since
// both the systemctl start and the readiness probe are no-ops against a
// unit that's already active.
func (r *Runtime) Start(ctx context.Context, name string) error {
	if err := orchestrator.ValidateInstanceName(name); err != nil {
		return err
	}
	if _, stderr, err := r.run(ctx, systemctlBin, systemctlStartArgs(name)...); err != nil {
		return fmt.Errorf("systemctl start: %w (%s)", err, strings.TrimSpace(stderr))
	}

	deadline := time.Now().Add(startReadinessTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, stderr, err := r.run(probeCtx, systemdRunBin, readinessProbeArgs(name)...)
		cancel()
		if err == nil {
			return nil
		}
		lastErr = fmt.Errorf("readiness probe: %w (%s)", err, strings.TrimSpace(stderr))
		time.Sleep(1 * time.Second)
	}
	logs, _, _ := r.run(context.Background(), journalctlBin, "-u", unitName(name), "-n", "50", "--no-pager")
	return fmt.Errorf("instance %s failed to become ready within %s: %v\nrecent journal:\n%s",
		name, startReadinessTimeout, lastErr, logs)
}

// Stop cleanly powers instance name off — rootfs and metadata are kept
// (the mechanism behind /suspend). Idempotent: `systemctl stop` on an
// already-inactive unit is itself a no-op success.
func (r *Runtime) Stop(ctx context.Context, name string) error {
	if err := orchestrator.ValidateInstanceName(name); err != nil {
		return err
	}
	if _, stderr, err := r.run(ctx, systemctlBin, systemctlStopArgs(name)...); err != nil {
		return fmt.Errorf("systemctl stop: %w (%s)", err, strings.TrimSpace(stderr))
	}
	return nil
}

// Terminate force-stops instance name with no clean-shutdown attempt.
// Idempotent, same reasoning as Stop.
func (r *Runtime) Terminate(ctx context.Context, name string) error {
	if err := orchestrator.ValidateInstanceName(name); err != nil {
		return err
	}
	if _, stderr, err := r.run(ctx, systemctlBin, systemctlKillArgs(name)...); err != nil {
		return fmt.Errorf("systemctl kill: %w (%s)", err, strings.TrimSpace(stderr))
	}
	return nil
}

// Destroy force-stops (if running) and permanently removes instance name's
// rootfs, unit drop-in, and generated secrets. name is re-validated (not
// trusted just because it should already have passed ResolveInstanceName
// upstream) and every path removed is re-checked to resolve under this
// Runtime's own roots before anything is deleted — see validateUnderRoot's
// doc comment. Idempotent: a rootfs/state dir that's already gone is
// success, not an error.
func (r *Runtime) Destroy(ctx context.Context, name string) error {
	if err := orchestrator.ValidateInstanceName(name); err != nil {
		return err
	}
	rfPath := rootfsPath(r.cfg, name)
	if err := validateUnderRoot(r.cfg.MachinesRoot, rfPath); err != nil {
		return err
	}
	stateDir := instanceStateDir(r.cfg, name)
	if err := validateUnderRoot(filepath.Join(r.cfg.StateRoot, "instances"), stateDir); err != nil {
		return err
	}

	// Force-stop first so nothing holds the rootfs open while it's removed.
	// Errors ignored: an instance that's already stopped (or never
	// started) is exactly the idempotent case this is meant to tolerate.
	r.run(ctx, systemctlBin, systemctlKillArgs(name)...)

	if _, stderr, err := r.run(ctx, machinectlBin, machinectlRemoveArgs(name)...); err != nil {
		if !alreadyGone(stderr) {
			return fmt.Errorf("machinectl remove: %w (%s)", err, strings.TrimSpace(stderr))
		}
	}

	if err := orchestrator.ShredSecrets(r.cfg.StateRoot, name); err != nil {
		return fmt.Errorf("shred secrets: %w", err)
	}
	if err := os.RemoveAll(stateDir); err != nil {
		return fmt.Errorf("remove instance state dir: %w", err)
	}
	if err := os.RemoveAll(dropInDir(name)); err != nil {
		return fmt.Errorf("remove drop-in: %w", err)
	}
	// Best-effort: a failed reload here doesn't undo anything already
	// removed above, it just means systemd's unit cache is briefly stale.
	r.run(ctx, systemctlBin, systemctlDaemonReloadArgs()...)
	return nil
}

// alreadyGone reports whether a machinectl stderr message indicates the
// target simply doesn't exist (already removed) rather than a real failure
// — the idempotent-Destroy case.
func alreadyGone(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "no machine") || strings.Contains(s, "no image") ||
		strings.Contains(s, "does not exist") || strings.Contains(s, "no such")
}

// List reports every instance systemd currently knows about (via its own
// unit list — not an in-memory cache), regardless of which process created
// it.
func (r *Runtime) List(ctx context.Context) ([]orchestrator.InstanceInfo, error) {
	stdout, stderr, err := r.run(ctx, systemctlBin, systemctlListInstancesArgs()...)
	if err != nil {
		return nil, fmt.Errorf("systemctl list-units: %w (%s)", err, strings.TrimSpace(stderr))
	}

	var infos []orchestrator.InstanceInfo
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		unit := strings.Fields(line)[0]
		if !strings.HasPrefix(unit, unitPrefix) || !strings.HasSuffix(unit, ".service") {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(unit, unitPrefix), ".service")
		if name == "" {
			continue // the bare template unit itself, not an instance
		}
		infos = append(infos, r.instanceInfo(ctx, name))
	}
	return infos, nil
}

// instanceInfo queries one instance's live state/memory — best-effort: a
// failed query leaves the corresponding field at its zero value rather than
// failing the whole List call over one instance's transient query error.
func (r *Runtime) instanceInfo(ctx context.Context, name string) orchestrator.InstanceInfo {
	info := orchestrator.InstanceInfo{Name: name}
	if out, _, err := r.run(ctx, systemctlBin, systemctlShowActiveStateArgs(name)...); err == nil {
		parts := strings.Split(strings.TrimSpace(out), "\n")
		if len(parts) >= 1 {
			info.State = normalizeState(parts[0])
		}
		if len(parts) >= 3 {
			if t, err := time.Parse("Mon 2006-01-02 15:04:05 MST", strings.TrimSpace(parts[2])); err == nil {
				info.Since = t
			}
		}
	}
	if out, _, err := r.run(ctx, systemctlBin, systemctlShowMemArgs(name)...); err == nil {
		if v, err := strconv.ParseUint(strings.TrimSpace(out), 10, 64); err == nil {
			info.MemoryCurrent = v
		}
	}
	return info
}

// normalizeState maps systemd's own ActiveState vocabulary to the plain
// terms InstanceInfo.State documents.
func normalizeState(activeState string) string {
	switch activeState {
	case "active":
		return "running"
	case "inactive":
		return "stopped"
	case "failed":
		return "failed"
	default:
		return activeState
	}
}

// Exec runs one buffered command inside instance name via a non-streaming
// systemd-run invocation — for one-shot /status-style commands, not a turn.
func (r *Runtime) Exec(ctx context.Context, name string, argv []string) (stdout, stderr string, code int, err error) {
	if err := orchestrator.ValidateInstanceName(name); err != nil {
		return "", "", -1, err
	}
	cmd := exec.CommandContext(ctx, systemdRunBin, execArgs(name, argv)...)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			return out.String(), errBuf.String(), exitErr.ExitCode(), nil
		}
		return "", "", -1, fmt.Errorf("systemd-run exec: %w (%s)", runErr, strings.TrimSpace(errBuf.String()))
	}
	return out.String(), errBuf.String(), 0, nil
}

// StartTurn runs one streaming agent turn inside instance name. Any stale
// turn unit left over from a crashed orchestrator is stopped first (errors
// ignored — the common case is there being none at all). The returned
// *orchestrator.Turn wraps the systemd-run client process's own stdio via
// subagent.AttachChild — the remote transient unit itself keeps running
// independent of this local client (see turnRunArgs' doc comment); UnitName
// is how a caller stops it from the outside.
func (r *Runtime) StartTurn(ctx context.Context, name string, spec orchestrator.TurnSpec) (*orchestrator.Turn, error) {
	if err := orchestrator.ValidateInstanceName(name); err != nil {
		return nil, err
	}
	r.run(ctx, systemctlBin, systemctlStopTurnArgs(name)...)

	cmd := exec.CommandContext(ctx, systemdRunBin, turnRunArgs(r.cfg, name, spec)...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("turn stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("turn stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start turn: %w", err)
	}
	return &orchestrator.Turn{
		ChildProcess: subagent.AttachChild(stdin, stdout),
		UnitName:     turnUnitName(name),
	}, nil
}

// StopTurn stops one specific in-flight turn by its unit name (Turn.UnitName)
// without touching the rest of the instance — the mechanism /kill and
// /suspend use to interrupt a running turn. Idempotent: `systemctl stop` on
// an already-gone/unknown unit is itself a no-op success.
func (r *Runtime) StopTurn(ctx context.Context, unitName string) error {
	if _, stderr, err := r.run(ctx, systemctlBin, systemctlStopUnitByNameArgs(unitName)...); err != nil {
		return fmt.Errorf("systemctl stop turn: %w (%s)", err, strings.TrimSpace(stderr))
	}
	return nil
}

// StopOrphanedTurn stops the turn unit that would exist for instance name
// (see turnUnitName) — used only during restart reconciliation.
func (r *Runtime) StopOrphanedTurn(ctx context.Context, name string) error {
	return r.StopTurn(ctx, turnUnitName(name))
}

// dirSize sums the size of every regular file under root — used to measure
// the golden image's on-disk footprint before a clone (orchestrator-host's ext4 root
// means every clone is a full recursive copy, not a CoW snapshot).
func dirSize(root string) (uint64, error) {
	var total uint64
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			total += uint64(info.Size())
		}
		return nil
	})
	return total, err
}

// freeBytes reports available (not just total) free space at path, via
// statfs — Create's disk-space precheck.
func freeBytes(path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return uint64(stat.Bavail) * uint64(stat.Bsize), nil
}
