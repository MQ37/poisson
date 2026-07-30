package sandbox

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FakeDriver is an in-memory Driver test double — no subprocess, no real
// podman. Same idiom as provider.FakeProvider (internal/provider/fake.go):
// pre-programmed/observable behavior instead of a real backend, so this
// package and its callers (BashTool's sandboxId routing, in a later step)
// can be tested without a podman install anywhere in CI. Exported so other
// packages' tests can construct one directly, the same way they already
// import provider.FakeProvider.
type FakeDriver struct {
	mu     sync.Mutex
	nextID int
	alive  map[string]bool
	// ExecFn, when set, overrides the default echo-style Exec behavior —
	// lets a test script a specific stdout/stderr/exitCode/err per call.
	ExecFn func(ctx context.Context, id, cmd, workdir string) (stdout, stderr string, exitCode int, err error)
}

// NewFakeDriver returns a FakeDriver with no containers yet.
func NewFakeDriver() *FakeDriver {
	return &FakeDriver{alive: make(map[string]bool)}
}

func (f *FakeDriver) Create(_ context.Context, _ CreateOpts) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	id := "fake-" + strconv.Itoa(f.nextID)
	f.alive[id] = true
	return id, nil
}

func (f *FakeDriver) Exec(ctx context.Context, id, cmd, workdir string, _ time.Duration) (string, string, int, error) {
	f.mu.Lock()
	alive := f.alive[id]
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
	return f.alive[id], nil
}

func (f *FakeDriver) Kill(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.alive[id] {
		return fmt.Errorf("fakeDriver: no such container %q", id)
	}
	delete(f.alive, id)
	return nil
}

// MarkDead simulates a container dying (or being reaped/killed) outside the
// normal Kill path — e.g. a parent session's idle-reap sweep, or the
// container crashing — without going through this driver's own Kill.
// Manager.Owns still returns true afterward (ownership and liveness are
// separate questions); Manager.Alive is what should go false. A test
// exercising the subagent case ("authorized id, but now dead") uses this
// directly on a FakeDriver shared between a parent's and a child's Manager.
func (f *FakeDriver) MarkDead(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.alive, id)
}
