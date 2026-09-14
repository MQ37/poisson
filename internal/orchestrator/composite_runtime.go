package orchestrator

import (
	"context"
	"fmt"
	"sync"
)

// CompositeRuntime implements Runtime by dispatching each call to the
// Runtime registered for that instance's kind ("box" -> nspawn.Runtime,
// "host" -> hostrt.Runtime). Core is entirely unaware this indirection
// exists — it just calls Runtime methods as always, never seeing kind
// anywhere except through the one narrow, documented exception in
// Core.handleNew (see docs/orchestrator-host-mode-plan.md §3.3).
type CompositeRuntime struct {
	mu     sync.Mutex
	byKind map[string]Runtime // "box" -> nspawn.Runtime, "host" -> hostrt.Runtime
	kindOf map[string]string  // instance name -> which kind owns it
}

// NewCompositeRuntime returns a CompositeRuntime dispatching to byKind.
func NewCompositeRuntime(byKind map[string]Runtime) *CompositeRuntime {
	return &CompositeRuntime{
		byKind: byKind,
		kindOf: make(map[string]string),
	}
}

var _ Runtime = (*CompositeRuntime)(nil)

// RehydrateKindOf seeds kindOf from persisted instance metadata. kindOf is
// only ever in-memory — after an orchestrator restart, a freshly
// constructed CompositeRuntime's kindOf map starts empty even though the
// instances themselves (and their Kind) already exist on disk, which would
// otherwise make every reconcile-path call (Start/Stop/StopOrphanedTurn/
// Destroy on an existing instance, none of which go through CreateKind)
// fail with "unknown instance". Called once by Core.reconcile before it
// touches any existing instance — see that function's own comment for why
// this is the second (and only other) narrow, documented place in Core
// that knows CompositeRuntime exists.
func (c *CompositeRuntime) RehydrateKindOf(metas []InstanceMeta) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, m := range metas {
		kind := m.Kind
		if IsBoxKind(kind) {
			kind = KindBox
		}
		c.kindOf[m.Name] = kind
	}
}

// backend returns the Runtime for kind, or an error naming the unknown kind
// — this should never happen for a kind that's actually reachable through
// Core's command dispatch, but a clear error beats a nil-pointer panic if
// it ever does.
func (c *CompositeRuntime) backend(kind string) (Runtime, error) {
	rt, ok := c.byKind[kind]
	if !ok {
		return nil, fmt.Errorf("compositeRuntime: no backend registered for kind %q", kind)
	}
	return rt, nil
}

// backendFor looks up which backend owns an already-created instance name.
func (c *CompositeRuntime) backendFor(name string) (Runtime, error) {
	c.mu.Lock()
	kind, ok := c.kindOf[name]
	c.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("compositeRuntime: unknown instance %q", name)
	}
	return c.backend(kind)
}

// CreateKind creates spec.Name under kind's backend. Unlike every other
// method here, this is not part of the Runtime interface (plain Create has
// no kind parameter, and no other backend needs one) — it is the one method
// that doesn't just look up kindOf[name] (nothing there yet); it checks the
// name isn't already used under ANY kind first, so "/new-box x" and
// "/new-host x" can never silently collide in Core's single flat byName
// map.
func (c *CompositeRuntime) CreateKind(ctx context.Context, kind string, spec InstanceSpec) error {
	rt, err := c.backend(kind)
	if err != nil {
		return err
	}
	c.mu.Lock()
	if _, exists := c.kindOf[spec.Name]; exists {
		c.mu.Unlock()
		return fmt.Errorf("compositeRuntime: instance %q already exists", spec.Name)
	}
	c.mu.Unlock()

	if err := rt.Create(ctx, spec); err != nil {
		return err
	}
	c.mu.Lock()
	c.kindOf[spec.Name] = kind
	c.mu.Unlock()
	return nil
}

// Create implements Runtime by defaulting to the "box" kind — satisfies the
// interface for any caller that doesn't know about kinds at all (e.g. a
// FakeRuntime-shaped test harness swapped in for CompositeRuntime
// wholesale). Core itself always goes through CreateKind via handleNew.
func (c *CompositeRuntime) Create(ctx context.Context, spec InstanceSpec) error {
	return c.CreateKind(ctx, KindBox, spec)
}

func (c *CompositeRuntime) Start(ctx context.Context, name string) error {
	rt, err := c.backendFor(name)
	if err != nil {
		return err
	}
	return rt.Start(ctx, name)
}

func (c *CompositeRuntime) Stop(ctx context.Context, name string) error {
	rt, err := c.backendFor(name)
	if err != nil {
		return err
	}
	return rt.Stop(ctx, name)
}

func (c *CompositeRuntime) Terminate(ctx context.Context, name string) error {
	rt, err := c.backendFor(name)
	if err != nil {
		return err
	}
	return rt.Terminate(ctx, name)
}

// Destroy routes to name's owning backend, then clears kindOf on success —
// after this, name is available again under any kind.
func (c *CompositeRuntime) Destroy(ctx context.Context, name string) error {
	rt, err := c.backendFor(name)
	if err != nil {
		return err
	}
	if err := rt.Destroy(ctx, name); err != nil {
		return err
	}
	c.mu.Lock()
	delete(c.kindOf, name)
	c.mu.Unlock()
	return nil
}

// List concatenates every backend's List() — a box and a host instance both
// show up in one combined report.
func (c *CompositeRuntime) List(ctx context.Context) ([]InstanceInfo, error) {
	c.mu.Lock()
	backends := make([]Runtime, 0, len(c.byKind))
	for _, rt := range c.byKind {
		backends = append(backends, rt)
	}
	c.mu.Unlock()

	var out []InstanceInfo
	for _, rt := range backends {
		infos, err := rt.List(ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, infos...)
	}
	return out, nil
}

func (c *CompositeRuntime) Exec(ctx context.Context, name string, argv []string) (stdout, stderr string, code int, err error) {
	rt, err := c.backendFor(name)
	if err != nil {
		return "", "", -1, err
	}
	return rt.Exec(ctx, name, argv)
}

func (c *CompositeRuntime) StartTurn(ctx context.Context, name string, spec TurnSpec) (*Turn, error) {
	rt, err := c.backendFor(name)
	if err != nil {
		return nil, err
	}
	return rt.StartTurn(ctx, name, spec)
}

func (c *CompositeRuntime) StopTurn(ctx context.Context, unitName string) error {
	// unitName alone doesn't carry which backend owns it (both nspawn and
	// hostrt derive "px-turn-<instance>" the same way) — every backend's
	// StopTurn is idempotent (stopping an unknown/already-gone unit is
	// success), so this tries each one rather than needing a second index.
	c.mu.Lock()
	backends := make([]Runtime, 0, len(c.byKind))
	for _, rt := range c.byKind {
		backends = append(backends, rt)
	}
	c.mu.Unlock()

	var firstErr error
	for _, rt := range backends {
		if err := rt.StopTurn(ctx, unitName); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (c *CompositeRuntime) StopOrphanedTurn(ctx context.Context, name string) error {
	rt, err := c.backendFor(name)
	if err != nil {
		return err
	}
	return rt.StopOrphanedTurn(ctx, name)
}
