package sandbox

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Sandbox is one live sandbox container tracked by a Manager.
type Sandbox struct {
	ID        string
	HostPath  string
	CreatedAt time.Time
	LastUsed  time.Time
}

// Manager wraps a Driver with ownership tracking: a sandboxId is only ever
// usable through the Manager instance that created it, one explicitly told
// about it via Authorize (the subagent allow-list case — see
// POISSON_SUBAGENT_SANDBOXES in docs/sandbox-plan.md), or — when
// EnableDiscovery was called — one this Manager discovers on demand via the
// Driver's own List (any live sandbox any process created, found by exact
// name; see "Crash recovery" in docs/sandbox-plan.md). A sandboxId is
// always per-call model input, never trusted on its own — every Exec/Kill
// checks ownership before any Driver call runs.
//
// Discovery defaults off and is enabled only for a top-level session's own
// Manager (cmd/px/main.go's newSandboxManager) — a subagent's Manager
// (resolveChildSandboxManager) never enables it, so the parent-authorizes-
// explicitly rule for subagents is unaffected by this.
type Manager struct {
	driver Driver

	// sessionID is stamped onto every sandbox this Manager creates (as
	// CreateOpts.SessionID, then a podman label) — purely informational,
	// shown by list_sandboxes, never used for access control.
	sessionID string
	// discovery, when true, lets Get/Owns fall back to the Driver's List on a
	// local-map miss and adopt a live, matching, running sandbox found there.
	discovery bool

	mu        sync.Mutex
	sandboxes map[string]*Sandbox
}

// NewManager wraps driver in a Manager with an empty ownership set and
// discovery disabled.
func NewManager(driver Driver) *Manager {
	return &Manager{driver: driver, sandboxes: make(map[string]*Sandbox)}
}

// SetSessionID records id as the label stamped on every sandbox this
// Manager creates from now on (see CreateOpts.SessionID). Purely
// informational — never checked by Owns/Get/Exec.
func (m *Manager) SetSessionID(id string) { m.sessionID = id }

// EnableDiscovery turns on the Driver.List fallback described on the
// Manager doc comment. Call this only for a Manager meant to represent a
// real, top-level session — never for a subagent's Manager, which must stay
// limited to exactly what its parent Authorize'd.
func (m *Manager) EnableDiscovery() { m.discovery = true }

// Create starts a new sandbox via the underlying Driver and records it as
// owned by this Manager. Stamps this Manager's own sessionID onto opts
// before the Driver ever sees it — a caller (e.g. CreateSandboxTool) never
// sets SessionID itself. Returns a value copy — see Get's doc comment for
// why callers never get a live pointer into the tracked map.
func (m *Manager) Create(ctx context.Context, opts CreateOpts) (Sandbox, error) {
	opts.SessionID = m.sessionID
	id, err := m.driver.Create(ctx, opts)
	if err != nil {
		return Sandbox{}, fmt.Errorf("create sandbox: %w", err)
	}
	now := time.Now()
	sb := &Sandbox{ID: id, HostPath: opts.HostPath, CreatedAt: now, LastUsed: now}
	m.mu.Lock()
	m.sandboxes[id] = sb
	m.mu.Unlock()
	return *sb, nil
}

// Authorize records sb as owned without this Manager having created it —
// the mechanism a subagent's own Manager uses for a sandboxId its parent
// explicitly authorized: the subagent process never called Create, so it
// has no record of the sandbox until told about it directly. Takes sb by
// value and stores its own copy, so the caller's copy (e.g. one it got from
// the parent Manager's own Create/Get) is never aliased into this
// Manager's map.
func (m *Manager) Authorize(sb Sandbox) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := sb
	m.sandboxes[sb.ID] = &cp
}

// Owns reports whether id was created by (or explicitly Authorize'd on)
// this Manager, or — with discovery enabled — is a live sandbox any process
// created that this Manager can find by exact name. Callers must check this
// before doing anything else with an id that came from model input — a
// hallucinated or foreign id must be rejected here, not discovered as a
// confusing Driver error later.
func (m *Manager) Owns(id string) bool {
	_, ok := m.Get(id)
	return ok
}

// Get returns a value copy of the tracked Sandbox for id, if owned or
// discoverable (see Owns). Never returns the live pointer held in the map:
// Exec mutates LastUsed under m.mu on every call, so a caller holding a raw
// pointer from an earlier Get could race with that mutation the moment more
// than one goroutine touches the same sandboxId — real once bash's
// sandboxId routing dispatches concurrent batch calls, not just in today's
// single-goroutine tests.
func (m *Manager) Get(id string) (Sandbox, bool) {
	m.mu.Lock()
	sb, ok := m.sandboxes[id]
	var cp Sandbox
	if ok {
		cp = *sb // copy while still holding m.mu — Exec mutates LastUsed under the same lock
	}
	m.mu.Unlock()
	if ok {
		return cp, true
	}
	return m.attach(id)
}

// attach is Get's discovery fallback (see EnableDiscovery): on a local-map
// miss, ask the Driver for every live sandbox it knows about and adopt one
// whose name matches id and is still running — this is what lets a fresh
// process (after a crash, or a different session entirely) resume using a
// sandboxId it never itself created. A non-running match is refused, not
// silently adopted — same reasoning Alive already applies elsewhere: a dead
// container is not a usable sandbox. Bounded by a short timeout so a wedged
// podman can't hang Owns/Get/Exec forever.
func (m *Manager) attach(id string) (Sandbox, bool) {
	if !m.discovery {
		return Sandbox{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	infos, err := m.driver.List(ctx)
	if err != nil {
		return Sandbox{}, false
	}
	for _, info := range infos {
		if info.ID != id || !info.Running {
			continue
		}
		sb := &Sandbox{ID: info.ID, HostPath: info.HostPath, CreatedAt: info.CreatedAt, LastUsed: time.Now()}
		m.mu.Lock()
		m.sandboxes[id] = sb
		m.mu.Unlock()
		return *sb, true
	}
	return Sandbox{}, false
}

// List reports every live sandbox this Manager's Driver knows about,
// system-wide — not just ones this Manager owns — for the list_sandboxes
// tool. Browsing never grants access: Owns/Get still gate everything else.
func (m *Manager) List(ctx context.Context) ([]Info, error) {
	return m.driver.List(ctx)
}

// Exec runs cmd inside sandbox id, rejecting any id this Manager doesn't
// own (or, with discovery, can't find) before the Driver ever sees it.
func (m *Manager) Exec(ctx context.Context, id, cmd, workdir string, timeout time.Duration) (stdout, stderr string, exitCode int, err error) {
	if _, ok := m.Get(id); !ok {
		return "", "", -1, fmt.Errorf("sandbox %q not found", id)
	}
	stdout, stderr, exitCode, err = m.driver.Exec(ctx, id, cmd, workdir, timeout)
	m.mu.Lock()
	if sb, ok := m.sandboxes[id]; ok {
		sb.LastUsed = time.Now()
	}
	m.mu.Unlock()
	return stdout, stderr, exitCode, err
}

// Alive reports whether sandbox id is owned AND still actually running, per
// the underlying Driver. A subagent must check this before trusting a
// parent-authorized id — the parent, or the idle reaper, may have killed it
// since spawn.
func (m *Manager) Alive(ctx context.Context, id string) (bool, error) {
	if !m.Owns(id) {
		return false, fmt.Errorf("sandbox %q not found", id)
	}
	return m.driver.Inspect(ctx, id)
}

// Kill stops sandbox id via the Driver and drops it from this Manager's
// ownership set.
func (m *Manager) Kill(ctx context.Context, id string) error {
	if !m.Owns(id) {
		return fmt.Errorf("sandbox %q not found", id)
	}
	if err := m.driver.Kill(ctx, id); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.sandboxes, id)
	m.mu.Unlock()
	return nil
}
