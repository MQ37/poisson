package hostrt

import (
	"github.com/mq37/poisson/internal/orchestrator"
)

// turnUnitPrefix mirrors nspawn's own turnUnitPrefix -- CompositeRuntime
// guarantees instance names are unique across both kinds, so reusing the
// same naming convention here creates no collision risk.
const turnUnitPrefix = "px-turn-"

// turnUnitName returns the transient unit name systemd-run creates for one
// host-mode turn against instance name.
func turnUnitName(name string) string {
	return turnUnitPrefix + name
}

// turnRunArgs builds the systemd-run invocation StartTurn execs: identical
// to nspawn's turnRunArgs except it has no --machine= flag at all -- the
// turn's transient unit runs directly on the host, not inside any
// container. Everything downstream (unit naming, --pipe/--collect/--wait,
// the two "--"s separating systemd-run's own flags / px's flags / the
// verbatim message) is exactly the same mechanism, reused essentially for
// free (docs/orchestrator-host-mode-plan.md §3.4).
func turnRunArgs(cfg Config, name string, spec orchestrator.TurnSpec) []string {
	args := []string{
		"--unit=" + turnUnitName(name),
		"--pipe", "--collect", "--wait",
		"--setenv=HOME=/root",
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

// systemctlStopUnitByNameArgs mirrors nspawn's own helper of the same name
// -- stops an already-known unit name directly (Turn.UnitName).
func systemctlStopUnitByNameArgs(unitName string) []string {
	return []string{"stop", unitName + ".service"}
}
