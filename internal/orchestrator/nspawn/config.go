// Package nspawn implements orchestrator.Runtime by shelling out to
// systemd-nspawn/machinectl/systemd-run — one real-root, host-networked
// nspawn container per instance. See docs/orchestrator-plan.md §3/§7 Step
// 12 for the full design and the specific reasoning behind every flag.
package nspawn

// Config is the fixed, host-wide configuration every Runtime method needs —
// paths and resource limits, none of it instance-specific. Resource limits
// are copied into every instance's own systemd drop-in (not applied
// globally) so a future per-instance override is a one-field change, not a
// mechanism change — see docs/orchestrator-plan.md's assumption list.
type Config struct {
	// StateRoot is the orchestrator's own state directory, e.g.
	// /var/lib/px-orchestrate — instances/<name>/{instance.json,
	// config.toml, secrets/, work/} live under here (see
	// internal/orchestrator/layout.go). Distinct from MachinesRoot, which
	// holds the actual rootfs.
	StateRoot string
	// MachinesRoot is machinectl's own fixed image root, normally
	// /var/lib/machines. A field (not a hardcoded literal) only so tests
	// can point it at a temp directory.
	MachinesRoot string
	// GoldenImage is the golden rootfs's machinectl image name (e.g.
	// "px-golden") that Create clones from.
	GoldenImage string
	// PxBinPath is the host's static px binary, bind-mounted read-only into
	// every instance — see docs/orchestrator-plan.md §4 reuse decision 7.
	PxBinPath string

	// MemoryMax/CPUQuota/TasksMax are copied verbatim into every instance's
	// systemd drop-in (see instanceDropInContent). Zero swap on orchestrator-host
	// means a MemoryMax breach is an immediate OOM kill, not graceful
	// swapping — see Runtime.Start's readiness-failure diagnostics.
	MemoryMax string // e.g. "1200M"
	CPUQuota  string // e.g. "150%"
	TasksMax  string // e.g. "2048"
}

// DefaultConfig returns the production configuration from
// docs/orchestrator-plan.md M0/Step 12.
func DefaultConfig() Config {
	return Config{
		StateRoot:    "/var/lib/px-orchestrate",
		MachinesRoot: "/var/lib/machines",
		GoldenImage:  "px-golden",
		PxBinPath:    "/usr/local/bin/px",
		MemoryMax:    "1200M",
		CPUQuota:     "150%",
		TasksMax:     "2048",
	}
}
