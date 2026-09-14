package hostrt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mq37/poisson/internal/orchestrator"
	"github.com/mq37/poisson/internal/subagent"
)

// systemctlBin/systemdRunBin are package vars (not hardcoded literals),
// same idiom as nspawn.systemctlBin, so a future test double can override
// them.
var (
	systemctlBin  = "systemctl"
	systemdRunBin = "systemd-run"
)

// hostStateFileName is the marker written under an instance's own state
// directory (shared with nspawn's instances/<name> tree) that makes it a
// host instance -- its presence is exactly what List uses to tell a host
// instance apart from a box instance sharing the same parent directory
// (neither kind's Create knows about the other's existence).
const hostStateFileName = "host-state"

// Runtime implements orchestrator.Runtime by running turns directly on the
// orchestrator host -- no container, no isolation, real root. See
// docs/orchestrator-host-mode-plan.md §3.4.
type Runtime struct {
	cfg Config
}

// NewRuntime returns a Runtime for cfg.
func NewRuntime(cfg Config) *Runtime {
	return &Runtime{cfg: cfg}
}

var _ orchestrator.Runtime = (*Runtime)(nil)

func (r *Runtime) stateFilePath(name string) string {
	return filepath.Join(orchestrator.InstanceStateDir(r.cfg.StateRoot, name), hostStateFileName)
}

// readState returns "running"/"stopped", or "" if name isn't a known host
// instance at all (no marker file).
func (r *Runtime) readState(name string) string {
	data, err := os.ReadFile(r.stateFilePath(name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func (r *Runtime) writeState(name, state string) error {
	return orchestrator.WriteFileAtomic(r.stateFilePath(name), []byte(state), 0o600)
}

// Create builds the instance's state-dir layout (secrets/ stays unused and
// empty -- host mode uses the host's own auth.json directly, never a
// scoped copy; work/ becomes this instance's real host-side scratch cwd)
// and marks it as a host instance via the state marker file, initially
// stopped. No rootfs, no clone, no bind mounts -- there's nothing else to
// create.
func (r *Runtime) Create(_ context.Context, spec orchestrator.InstanceSpec) error {
	if err := orchestrator.ValidateInstanceName(spec.Name); err != nil {
		return err
	}
	if err := orchestrator.CreateInstanceLayout(r.cfg.StateRoot, spec.Name); err != nil {
		return fmt.Errorf("create instance layout: %w", err)
	}
	if err := r.writeState(spec.Name, "stopped"); err != nil {
		os.RemoveAll(orchestrator.InstanceStateDir(r.cfg.StateRoot, spec.Name))
		return fmt.Errorf("write host state marker: %w", err)
	}
	return nil
}

// Start flips the instance's bookkeeping bit to running -- no process runs
// between turns for any instance kind (see
// docs/orchestrator-plan.md assumption 10), so there is nothing to
// actually boot. Idempotent.
func (r *Runtime) Start(_ context.Context, name string) error {
	if err := orchestrator.ValidateInstanceName(name); err != nil {
		return err
	}
	return r.writeState(name, "running")
}

// Stop flips the instance's bookkeeping bit to stopped -- StartTurn then
// refuses to run against it (see StartTurn's own doc comment). Idempotent.
func (r *Runtime) Stop(_ context.Context, name string) error {
	if err := orchestrator.ValidateInstanceName(name); err != nil {
		return err
	}
	return r.writeState(name, "stopped")
}

// Terminate is identical to Stop -- there is no running process to force-
// kill between turns, only the bookkeeping bit to flip.
func (r *Runtime) Terminate(ctx context.Context, name string) error {
	return r.Stop(ctx, name)
}

// Destroy removes the instance's state directory. No rootfs to remove, no
// secrets to shred (host mode never wrote any). Idempotent: a state
// directory that's already gone is success.
func (r *Runtime) Destroy(_ context.Context, name string) error {
	if err := orchestrator.ValidateInstanceName(name); err != nil {
		return err
	}
	return os.RemoveAll(orchestrator.InstanceStateDir(r.cfg.StateRoot, name))
}

// List reports every known host instance from local state-dir bookkeeping
// -- there's no machinectl/systemctl entity representing "this host
// instance exists" the way a box instance's own unit does. MemoryCurrent
// is always 0: host-mode turns are deliberately NOT resource-limited (see
// docs/orchestrator-host-mode-plan.md §6.2), so there is no cgroup
// accounting scoped to just this instance to report in the first place.
func (r *Runtime) List(_ context.Context) ([]orchestrator.InstanceInfo, error) {
	root := filepath.Join(r.cfg.StateRoot, "instances")
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list instances dir: %w", err)
	}
	var infos []orchestrator.InstanceInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		state := r.readState(e.Name())
		if state == "" {
			continue // not a host instance (no marker file) -- e.g. a box instance's own directory
		}
		infos = append(infos, orchestrator.InstanceInfo{Name: e.Name(), State: state})
	}
	return infos, nil
}

// Exec runs argv directly on the host, no wrapping at all.
func (r *Runtime) Exec(ctx context.Context, _ string, argv []string) (stdout, stderr string, code int, err error) {
	if len(argv) == 0 {
		return "", "", -1, fmt.Errorf("hostrt: Exec called with empty argv")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			return out.String(), errBuf.String(), exitErr.ExitCode(), nil
		}
		return "", "", -1, fmt.Errorf("exec: %w", runErr)
	}
	return out.String(), errBuf.String(), 0, nil
}

// StartTurn runs one streaming agent turn directly on the host via
// systemd-run (turnRunArgs minus nspawn's --machine=). Refuses explicitly
// if the instance is suspended -- nspawn relies on the container itself
// being stopped to make that true implicitly; host mode has no container
// to rely on, so this check must be explicit here.
func (r *Runtime) StartTurn(ctx context.Context, name string, spec orchestrator.TurnSpec) (*orchestrator.Turn, error) {
	if err := orchestrator.ValidateInstanceName(name); err != nil {
		return nil, err
	}
	switch r.readState(name) {
	case "running":
	case "":
		return nil, fmt.Errorf("hostrt: no such host instance %q", name)
	default:
		return nil, fmt.Errorf("hostrt: instance %q is suspended, refusing to start a turn", name)
	}

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

// StopTurn stops one specific in-flight turn by its unit name
// (Turn.UnitName). Idempotent: `systemctl stop` on an already-gone/unknown
// unit is itself a no-op success.
func (r *Runtime) StopTurn(ctx context.Context, unitName string) error {
	cmd := exec.CommandContext(ctx, systemctlBin, systemctlStopUnitByNameArgs(unitName)...)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("systemctl stop turn: %w (%s)", err, strings.TrimSpace(errBuf.String()))
	}
	return nil
}

// StopOrphanedTurn stops the turn unit that would exist for instance name,
// if any -- used only during restart reconciliation.
func (r *Runtime) StopOrphanedTurn(ctx context.Context, name string) error {
	return r.StopTurn(ctx, turnUnitName(name))
}
