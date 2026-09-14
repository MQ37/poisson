// Package hostrt implements orchestrator.Runtime by running turns directly
// on the orchestrator host itself -- no container, no isolation, real root.
// See docs/orchestrator-host-mode-plan.md §3.4 for the full design.
package hostrt

// Config is a host-mode Runtime's fixed configuration. Deliberately no
// MachinesRoot/GoldenImage (no rootfs to clone) and no resource-limit
// fields (decided: host turns are never capped -- see
// docs/orchestrator-host-mode-plan.md §6.2 -- a host instance acts with the
// same authority an interactive operator has).
type Config struct {
	// PxBinPath is the px binary StartTurn execs -- the same binary already
	// running the orchestrator itself (normally on PATH), not bind-mounted
	// anywhere since there's no container boundary to cross.
	PxBinPath string
	// StateRoot is the orchestrator's own state directory (shared with
	// nspawn's instances -- see internal/orchestrator/layout.go). A host
	// instance's own entry is distinguished from a box instance's by the
	// presence of the host-state marker file this package writes (see
	// stateFilePath), not by any separate directory tree.
	StateRoot string
}

// DefaultConfig returns the production configuration.
func DefaultConfig() Config {
	return Config{
		PxBinPath: "/usr/local/bin/px",
		StateRoot: "/var/lib/px-orchestrate",
	}
}
