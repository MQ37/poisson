// Package orchestrator manages persistent, isolated, headless poisson agent
// instances, each reachable over a pluggable Frontend (Telegram first) and
// executed by a Runtime (systemd-nspawn first). See
// docs/orchestrator-plan.md for the full design and rationale.
package orchestrator

import (
	"context"
	"time"

	"github.com/mq37/poisson/internal/subagent"
)

// InstanceSpec is everything Create needs to bring up a brand-new instance:
// its already-resolved name (see ResolveInstanceName — Create never
// sanitizes on its own), initial provider/model pair, and which of the
// host's own provider credentials this instance may see at all. Never all
// of them by default — see docs/orchestrator-plan.md Step 15's threat model.
type InstanceSpec struct {
	Name                string
	Provider            string
	Model               string
	AuthorizedProviders []string // subset of host auth.json entries copied into this instance's own secrets/auth.json; empty = none
}

// TurnSpec is one turn: which session inside the instance's own poisson.db
// to run it against (conversation continuity across turns), the
// provider/model pair (so a per-instance /model switch takes effect
// immediately), the user's message, and whether risky bash auto-approves
// (--yolo) or round-trips an approval_request through the Turn (see
// docs/orchestrator-plan.md Step 8).
type TurnSpec struct {
	SessionID string
	Provider  string
	Model     string
	Message   string
	Yolo      bool
}

// InstanceInfo is one Runtime.List entry, always derived fresh from the
// backend (machinectl/systemctl) — never an in-memory cache. The backend's
// own List call is the source of truth across process restarts, the same
// pattern sandbox.Manager.EnableDiscovery already establishes for Podman
// sandboxes (see docs/orchestrator-plan.md §4 reuse decision 4).
type InstanceInfo struct {
	Name  string
	State string // e.g. "running" | "stopped" | "degraded" — nspawnRuntime's own normalization of systemctl's ActiveState/SubState
	Since time.Time
	// MemoryCurrent is the instance's current cgroup memory usage in bytes
	// (systemctl show -p MemoryCurrent), 0 if unavailable.
	MemoryCurrent uint64
}

// Turn is one in-flight agent turn's process handle. It embeds
// *subagent.ChildProcess (built via subagent.AttachChild, not subagent.Spawn
// — see AttachChild's own doc comment) so a caller drives it via
// ReadEvent/SendApprovalSafe/SendExpedite exactly like an in-process
// subagent, with zero protocol-translation code (see docs/orchestrator-plan.md
// §4 reuse decisions 1-2).
//
// Never call Kill()/Reap() on the embedded ChildProcess: an attached child
// has no local *exec.Cmd to signal (they're nil-guarded no-ops precisely so
// this mistake doesn't panic, not so it's a supported way to stop a turn).
// Process lifecycle for a systemd-run-spawned turn belongs to whichever
// Runtime started it — e.g. nspawnRuntime stops UnitName via systemctl.
type Turn struct {
	*subagent.ChildProcess
	// UnitName is the transient unit (e.g. "px-turn-<instance>") StartTurn
	// created — the handle a caller (or the Runtime itself) uses to stop
	// this specific turn from the outside.
	UnitName string
}

// Runtime is where an agent instance actually executes — one systemd-nspawn
// container per instance, real root, host networking (see
// docs/orchestrator-plan.md §3). nspawnRuntime
// (internal/orchestrator/nspawn) is the only production implementation;
// FakeRuntime is an in-memory double for testing Core without any container
// involved. Deliberately not internal/sandbox.Driver — see
// docs/orchestrator-plan.md §2.2 for the full reasoning (Driver fuses
// stop+remove, has no stop-only primitive /suspend needs, and execs as a
// uid-mapped non-root user).
//
// Every method takes a context.Context and must be idempotent: Stop on an
// already-stopped instance, Destroy on a rootfs that's already gone, and
// Start on an already-running instance are all success, not error —
// matching sandbox.Driver.Start's own documented idempotence.
type Runtime interface {
	// Create clones the golden rootfs into a brand-new instance per spec,
	// generates its per-instance secrets/config, and installs its systemd
	// unit — but does not start it (see Start). Cleans up its own partial
	// state (rootfs clone, generated secrets, installed unit) on any
	// failure rather than leaving a half-created instance behind.
	Create(ctx context.Context, spec InstanceSpec) error
	// Start boots instance name, polling for real readiness (systemctl
	// start returns before boot actually completes — see
	// docs/orchestrator-plan.md Step 12). Idempotent.
	Start(ctx context.Context, name string) error
	// Stop cleanly powers instance name off. The rootfs and its metadata
	// are kept — this is the mechanism behind /suspend. Idempotent.
	Stop(ctx context.Context, name string) error
	// Terminate force-stops instance name with no clean-shutdown attempt —
	// used when Stop's grace period would block a caller that needs the
	// instance down immediately (e.g. right before Destroy). Idempotent.
	Terminate(ctx context.Context, name string) error
	// Destroy force-stops (if running) and permanently removes instance
	// name's entire rootfs, unit, and generated secrets. Irreversible:
	// name must already be in ResolveInstanceName's sanitized form, and
	// every path removed must resolve under this Runtime's own instances
	// root — Destroy refuses rather than rm -rf'ing anything else, since it
	// is transitively reachable from untrusted Telegram input. Idempotent
	// (a rootfs that's already gone is success).
	Destroy(ctx context.Context, name string) error
	// List reports every instance this Runtime's backend currently knows
	// about (machinectl/systemctl, not an in-memory cache), regardless of
	// which process created it.
	List(ctx context.Context) ([]InstanceInfo, error)
	// Exec runs one buffered (non-streaming) command inside instance name
	// and returns its full output — for one-shot /status-style commands
	// (e.g. `px cost <session>`, see docs/orchestrator-plan.md §4 reuse
	// decision 6), not a turn.
	Exec(ctx context.Context, name string, argv []string) (stdout, stderr string, code int, err error)
	// StartTurn runs one streaming agent turn inside instance name per spec
	// and returns a *Turn. The returned Turn is still running — the caller
	// (or the Runtime's own bookkeeping) is responsible for eventually
	// waiting on or stopping it; StartTurn itself does not block for the
	// turn to finish.
	StartTurn(ctx context.Context, name string, spec TurnSpec) (*Turn, error)
	// StopTurn stops one specific in-flight turn (identified by the
	// Turn.UnitName StartTurn returned) without touching the rest of the
	// instance — the mechanism /kill and /suspend use to interrupt a
	// running turn before tearing down (or suspending) the instance itself.
	// Idempotent: stopping an already-finished or unknown turn unit is
	// success, not an error.
	StopTurn(ctx context.Context, unitName string) error
	// StopOrphanedTurn stops the turn unit that would exist for instance
	// name, if any — used only during restart reconciliation, when no live
	// *Turn handle exists to read UnitName off of (the orchestrator process
	// that held it is gone, see docs/orchestrator-plan.md Step 21). Each
	// Runtime implementation derives its own turn-unit naming convention
	// internally rather than leaking it across this interface. Idempotent,
	// same as StopTurn.
	StopOrphanedTurn(ctx context.Context, name string) error
}
