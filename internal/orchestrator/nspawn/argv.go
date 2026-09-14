package nspawn

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mq37/poisson/internal/orchestrator"
)

// unitPrefix names the shared systemd template unit every instance starts
// from: px-instance@<name>.service.
const unitPrefix = "px-instance@"

// turnUnitPrefix names one turn's transient systemd-run unit:
// px-turn-<name> (one at a time per instance — a stale one from a crashed
// orchestrator must be stopped before reuse, see Runtime.StartTurn).
const turnUnitPrefix = "px-turn-"

// unitName returns the instance's own systemd unit name, e.g.
// "px-instance@px-alpha.service".
func unitName(name string) string {
	return unitPrefix + name + ".service"
}

// turnUnitName returns the transient unit name systemd-run creates for one
// turn against instance name.
func turnUnitName(name string) string {
	return turnUnitPrefix + name
}

// templateUnitPath is where the shared instance template unit is installed
// — idempotently ensured to exist (see Runtime.ensureTemplateUnit) before
// the first instance is ever created.
const templateUnitPath = "/etc/systemd/system/px-instance@.service"

// dropInDir/dropInPath locate one instance's own resource-limit drop-in —
// per-instance (not a single wildcard drop-in shared by every instance) so
// a future per-instance resource override is a one-file rewrite, not a
// mechanism change.
func dropInDir(name string) string {
	return "/etc/systemd/system/" + unitPrefix + name + ".service.d"
}

func dropInPath(name string) string {
	return filepath.Join(dropInDir(name), "50-resources.conf")
}

// rootfsPath returns the instance's rootfs directory under cfg.MachinesRoot.
func rootfsPath(cfg Config, name string) string {
	return filepath.Join(cfg.MachinesRoot, name)
}

// instanceStateDir returns the instance's own state directory under
// cfg.StateRoot (see internal/orchestrator/layout.go's InstanceStateDir,
// duplicated here as a plain path join so this package doesn't need to
// import orchestrator just for one path helper it already has StateRoot
// for).
func instanceStateDir(cfg Config, name string) string {
	return filepath.Join(cfg.StateRoot, "instances", name)
}

// templateUnitContent is the shared systemd unit template every instance
// starts from — %i is systemd's own instance-name specifier for a template
// unit (px-instance@<name>.service), substituted by systemd itself at
// start time, not by this function.
//
// Every flag's reasoning (docs/orchestrator-plan.md §3/§7 Step 12):
//   - --private-users=no: real root, not user-namespace-remapped. Must be
//     explicit — machinectl start would otherwise route through
//     systemd-nspawn@.service, which forces -U. This is exactly why the
//     orchestrator owns this unit file instead of using machinectl start.
//   - --capability=all --drop-capability=CAP_SYS_MODULE: genuine root minus
//     the one trivial container-escape-to-host capability (loading a
//     kernel module). Two separate flags — "--capability=all,-X" is not
//     valid nspawn syntax.
//   - --boot: a real systemd PID 1 inside, not a single wrapped command.
//   - no --private-network: host networking (see §3.1) — zero new
//     firewall rules on a box reached only over SSH.
//   - --resolv-conf=bind-host: works with the guest's own
//     systemd-networkd/-resolved masked in the golden image (§3.1).
//   - --keep-unit: nspawn registers itself under the unit systemd already
//     created for it (this service) rather than a second nested scope.
func templateUnitContent(cfg Config) string {
	execStart := strings.Join([]string{
		"/usr/bin/systemd-nspawn",
		"--quiet",
		"--keep-unit",
		"--private-users=no",
		"--capability=all",
		"--drop-capability=CAP_SYS_MODULE",
		"--boot",
		"--machine=%i",
		"--directory=" + filepath.Join(cfg.MachinesRoot, "%i"),
		"--bind-ro=" + cfg.PxBinPath,
		"--bind-ro=" + filepath.Join(cfg.StateRoot, "instances", "%i", "secrets", "auth.json") + ":/root/.poisson/auth.json",
		"--bind-ro=" + filepath.Join(cfg.StateRoot, "instances", "%i", "config.toml") + ":/root/.poisson/config.toml",
		"--bind=" + filepath.Join(cfg.StateRoot, "instances", "%i", "work") + ":/work",
		"--resolv-conf=bind-host",
	}, " ")
	return "[Unit]\n" +
		"Description=px orchestrator instance %i\n" +
		"Documentation=https://github.com/mq37/poisson\n" +
		"\n" +
		"[Service]\n" +
		"Type=notify\n" +
		"ExecStart=" + execStart + "\n" +
		"KillMode=mixed\n" +
		"TimeoutStopSec=30\n"
}

// instanceDropInContent is one instance's resource-limit drop-in.
// MemoryAccounting=yes is required for MemoryCurrent to be queryable at all
// (Runtime.List's memory reporting); Delegate=yes costs nothing if unused
// and is required for nested podman to manage its own cgroup subtree (see
// docs/orchestrator-plan.md §3.2).
func instanceDropInContent(cfg Config) string {
	return "[Service]\n" +
		"MemoryMax=" + cfg.MemoryMax + "\n" +
		"CPUQuota=" + cfg.CPUQuota + "\n" +
		"TasksMax=" + cfg.TasksMax + "\n" +
		"MemoryAccounting=yes\n" +
		"Delegate=yes\n"
}

// cloneArgs builds `machinectl clone <golden> <name>`'s argv.
func cloneArgs(golden, name string) []string {
	return []string{"clone", golden, name}
}

// machinectlRemoveArgs builds `machinectl remove <name>`'s argv — removes
// an offline (not-running) machine image. Callers must ensure the instance
// is stopped first.
func machinectlRemoveArgs(name string) []string {
	return []string{"remove", name}
}

// machinectlShowStateArgs builds argv reporting one machine's raw State
// property (used alongside systemctl's own ActiveState for List/Start's
// readiness probe).
func machinectlShowStateArgs(name string) []string {
	return []string{"show", name, "-p", "State", "--value"}
}

// systemctlStartArgs/StopArgs/KillArgs/ShowMemArgs build argv for the
// instance's own template-derived unit (not a turn's transient one).
func systemctlStartArgs(name string) []string {
	return []string{"start", unitName(name)}
}

func systemctlStopArgs(name string) []string {
	return []string{"stop", unitName(name)}
}

// systemctlKillArgs forces an immediate stop with no clean-shutdown attempt
// — Terminate's mechanism, distinct from Stop's plain `systemctl stop`
// (which lets nspawn/systemd-inside shut down cleanly first).
func systemctlKillArgs(name string) []string {
	return []string{"kill", "--signal=SIGKILL", unitName(name)}
}

func systemctlShowMemArgs(name string) []string {
	return []string{"show", unitName(name), "-p", "MemoryCurrent", "--value"}
}

func systemctlShowActiveStateArgs(name string) []string {
	return []string{"show", unitName(name), "-p", "ActiveState,SubState,ActiveEnterTimestamp", "--value"}
}

// systemctlListInstancesArgs lists every unit matching this feature's
// template prefix, regardless of which process created it — List's source
// of truth, not an in-memory cache (see docs/orchestrator-plan.md §4 reuse
// decision 4).
func systemctlListInstancesArgs() []string {
	return []string{"list-units", unitPrefix + "*", "--all", "--no-legend", "--plain"}
}

func systemctlDaemonReloadArgs() []string {
	return []string{"daemon-reload"}
}

func systemctlStopTurnArgs(name string) []string {
	return []string{"stop", turnUnitName(name) + ".service"}
}

// systemctlStopUnitByNameArgs stops an already-known unit name directly
// (Turn.UnitName, e.g. "px-turn-px-alpha") — the Runtime.StopTurn mechanism,
// distinct from systemctlStopTurnArgs above (which re-derives the unit name
// from an instance name for the "stop any stale turn before starting a new
// one" case in StartTurn).
func systemctlStopUnitByNameArgs(unitName string) []string {
	return []string{"stop", unitName + ".service"}
}

// readinessProbeArgs builds `systemd-run --machine=<name> --pipe --wait --
// /bin/true`'s argv — an actual readiness probe, not just trusting
// `systemctl start`'s own (early) success return (see Runtime.Start's doc
// comment: systemctl start returns before boot actually completes).
func readinessProbeArgs(name string) []string {
	return []string{"--machine=" + name, "--pipe", "--wait", "--", "/bin/true"}
}

// execArgs builds a buffered one-shot systemd-run invocation inside
// instance name — Runtime.Exec's mechanism (e.g. `px cost <session>` for
// /status, see docs/orchestrator-plan.md §4 reuse decision 6), distinct
// from turnRunArgs' streaming --pipe use for an actual agent turn.
func execArgs(name string, argv []string) []string {
	args := []string{"--machine=" + name, "--pipe", "--wait", "--collect", "--"}
	return append(args, argv...)
}

// turnRunArgs builds the systemd-run invocation StartTurn execs: a
// streaming (--pipe), auto-cleaned-up (--collect), foreground-tracked
// (--wait) transient unit running `px -p --print-json` inside instance
// name. The leading "--" separates systemd-run's own flags from the
// command; px's own "--" (from cmd/px M1 Step 5) separates px's flags from
// spec.Message verbatim — two different "--"s for two different parsers,
// not a mistake.
func turnRunArgs(cfg Config, name string, spec orchestrator.TurnSpec) []string {
	args := []string{
		"--machine=" + name,
		"--unit=" + turnUnitName(name),
		"--pipe", "--collect", "--wait",
		"--setenv=HOME=/root",
		// Tells buildPrintAgent to omit set_title -- an orchestrator
		// instance's turn has no TUI window for it to rename (see
		// docs/orchestrator-host-mode-plan.md §3.2).
		"--setenv=POISSON_ORCHESTRATE_INSTANCE=1",
		"--",
		cfg.PxBinPath, "-p", "--print-json",
	}
	if spec.Yolo {
		args = append(args, "--yolo")
	}
	if spec.SessionID != "" {
		args = append(args, "--session", spec.SessionID)
	}
	if spec.Provider != "" && spec.Model != "" {
		args = append(args, "--model", spec.Provider+"/"+spec.Model)
	}
	args = append(args, "--", spec.Message)
	return args
}

// validateUnderRoot ensures resolved path p is genuinely inside root (via
// filepath.Clean + a trailing-separator-safe prefix check) before any
// operation that could remove it — defense in depth for Destroy, on top of
// orchestrator.ValidateInstanceName, since name is transitively untrusted
// (Telegram message text) all the way down to here.
func validateUnderRoot(root, p string) error {
	root = filepath.Clean(root)
	p = filepath.Clean(p)
	if p == root {
		return fmt.Errorf("refusing to operate on root itself: %s", p)
	}
	if !strings.HasPrefix(p, root+string(filepath.Separator)) {
		return fmt.Errorf("path %q escapes expected root %q", p, root)
	}
	return nil
}
