package sandbox

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// fakeContainer is one container tracked by FakeDriver — enough state to
// answer Exec/Inspect/Kill/List the way a real podman backend would,
// without a subprocess.
type fakeContainer struct {
	hostPath  string
	sessionID string
	createdAt time.Time
	alive     bool
}

// FakeDriver is an in-memory Driver test double — no subprocess, no real
// podman. Same idiom as provider.FakeProvider (internal/provider/fake.go):
// pre-programmed/observable behavior instead of a real backend, so this
// package and its callers (BashTool's sandboxId routing, in a later step)
// can be tested without a podman install anywhere in CI. Exported so other
// packages' tests can construct one directly, the same way they already
// import provider.FakeProvider.
type FakeDriver struct {
	mu         sync.Mutex
	containers map[string]*fakeContainer
	// ExecFn, when set, overrides the default echo-style Exec behavior —
	// lets a test script a specific stdout/stderr/exitCode/err per call.
	ExecFn func(ctx context.Context, id, cmd, workdir string) (stdout, stderr string, exitCode int, err error)
	// CreateErr, when set, makes every Create fail with this error instead
	// of succeeding — lets a test exercise a caller's failure/cleanup path
	// (e.g. CreateSandboxTool removing its scratch workspace when
	// Manager.Create fails).
	CreateErr error
	// CreateCalls records every CreateOpts this driver has been asked to
	// create, in order — lets a test confirm what a caller actually passed
	// through (image, mounts, env) without needing a real podman backend to
	// inspect.
	CreateCalls []CreateOpts
}

// NewFakeDriver returns a FakeDriver with no containers yet.
func NewFakeDriver() *FakeDriver {
	return &FakeDriver{containers: make(map[string]*fakeContainer)}
}

// Create resolves opts.Name through ResolveSandboxName (same policy the real
// podmanDriver uses) and rejects a name collision with a live container the
// same way `podman create --name` would — a real, testable error path, not
// just a happy-path echo.
func (f *FakeDriver) Create(_ context.Context, opts CreateOpts) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.CreateCalls = append(f.CreateCalls, opts)
	if f.CreateErr != nil {
		return "", f.CreateErr
	}
	name, err := ResolveSandboxName(opts.Name)
	if err != nil {
		return "", err
	}
	if c, ok := f.containers[name]; ok && c.alive {
		return "", fmt.Errorf("fakeDriver: name %q already in use", name)
	}
	f.containers[name] = &fakeContainer{
		hostPath:  opts.HostPath,
		sessionID: opts.SessionID,
		createdAt: time.Now(),
		alive:     true,
	}
	return name, nil
}

func (f *FakeDriver) Exec(ctx context.Context, id, cmd, workdir string, _ time.Duration) (string, string, int, error) {
	f.mu.Lock()
	c, ok := f.containers[id]
	alive := ok && c.alive
	fn := f.ExecFn
	f.mu.Unlock()
	if !alive {
		return "", "", -1, fmt.Errorf("fakeDriver: no such container %q", id)
	}
	if fn != nil {
		return fn(ctx, id, cmd, workdir)
	}
	// Default: pretend the command ran and echo it back, so a test that
	// doesn't care about specific output still has something to assert on.
	return strings.TrimSpace(cmd), "", 0, nil
}

func (f *FakeDriver) Inspect(_ context.Context, id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[id]
	return ok && c.alive, nil
}

// Start flips a tracked (alive or not) container back to alive — simulating
// `podman start` on a stopped-but-not-removed container. Errors only when
// id was never created at all (or was Kill'd, which deletes the record
// entirely) — matching how the real podmanDriver.Start behaves against a
// truly nonexistent container name. Idempotent: starting an already-alive
// container just succeeds, same as the real backend.
func (f *FakeDriver) Start(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[id]
	if !ok {
		return fmt.Errorf("fakeDriver: no such container %q", id)
	}
	c.alive = true
	return nil
}

// Kill removes container id regardless of alive/stopped state, matching the
// real podmanDriver's `podman rm -f -t 0` (which doesn't care whether the
// container is running). Only a truly nonexistent id errors.
func (f *FakeDriver) Kill(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.containers[id]; !ok {
		return fmt.Errorf("fakeDriver: no such container %q", id)
	}
	delete(f.containers, id)
	return nil
}

// List reports every tracked container, alive or MarkDead'd (a real podman
// `ps -a` shows stopped containers too) — Kill removes the record entirely,
// matching `podman rm -f` actually deleting it.
func (f *FakeDriver) List(_ context.Context) ([]Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	infos := make([]Info, 0, len(f.containers))
	for id, c := range f.containers {
		infos = append(infos, Info{
			ID:        id,
			HostPath:  c.hostPath,
			SessionID: c.sessionID,
			CreatedAt: c.createdAt,
			Running:   c.alive,
		})
	}
	return infos, nil
}

// MarkDead simulates a container dying (or being reaped/killed) outside the
// normal Kill path — e.g. a parent session's idle-reap sweep, or the
// container crashing — without going through this driver's own Kill.
// Manager.Owns still returns true afterward (ownership and liveness are
// separate questions); Manager.Alive is what should go false. The record
// itself is kept (alive flips to false, not deleted) — same as a real
// stopped-but-not-removed container still showing up in `podman ps -a`. A
// test exercising the subagent case ("authorized id, but now dead") uses
// this directly on a FakeDriver shared between a parent's and a child's
// Manager.
func (f *FakeDriver) MarkDead(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.containers[id]; ok {
		c.alive = false
	}
}
