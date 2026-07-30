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
// usable through the Manager instance that created it, or one explicitly
// told about it via Authorize (the subagent allow-list case — see
// POISSON_SUBAGENT_SANDBOXES in docs/sandbox-plan.md). A sandboxId is
// always per-call model input, never trusted on its own — every Exec/Kill
// checks ownership before any Driver call runs.
type Manager struct {
	driver Driver

	mu        sync.Mutex
	sandboxes map[string]*Sandbox
}

// NewManager wraps driver in a Manager with an empty ownership set.
func NewManager(driver Driver) *Manager {
	return &Manager{driver: driver, sandboxes: make(map[string]*Sandbox)}
}

// Create starts a new sandbox via the underlying Driver and records it as
// owned by this Manager. Returns a value copy — see Get's doc comment for
// why callers never get a live pointer into the tracked map.
func (m *Manager) Create(ctx context.Context, opts CreateOpts) (Sandbox, error) {
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
// this Manager. Callers must check this before doing anything else with an
// id that came from model input — a hallucinated or foreign id must be
// rejected here, not discovered as a confusing Driver error later.
func (m *Manager) Owns(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.sandboxes[id]
	return ok
}

// Get returns a value copy of the tracked Sandbox for id, if owned. Never
// returns the live pointer held in the map: Exec mutates LastUsed under
// m.mu on every call, so a caller holding a raw pointer from an earlier Get
// could race with that mutation the moment more than one goroutine touches
// the same sandboxId — real once bash's sandboxId routing dispatches
// concurrent batch calls, not just in today's single-goroutine tests.
func (m *Manager) Get(id string) (Sandbox, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sb, ok := m.sandboxes[id]
	if !ok {
		return Sandbox{}, false
	}
	return *sb, true
}

// Exec runs cmd inside sandbox id, rejecting any id this Manager doesn't
// own before the Driver ever sees it.
func (m *Manager) Exec(ctx context.Context, id, cmd, workdir string, timeout time.Duration) (stdout, stderr string, exitCode int, err error) {
	m.mu.Lock()
	sb, ok := m.sandboxes[id]
	m.mu.Unlock()
	if !ok {
		return "", "", -1, fmt.Errorf("sandbox %q not found", id)
	}
	stdout, stderr, exitCode, err = m.driver.Exec(ctx, id, cmd, workdir, timeout)
	m.mu.Lock()
	sb.LastUsed = time.Now()
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
