package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/mq37/poisson/internal/subagent"
)

// TurnScript is one scripted turn's event sequence for FakeRuntime.
// StartTurn — the events its returned *Turn's ReadEvent yields, in order.
// Hang simulates a stuck turn process: ReadEvent blocks forever instead of
// reaching EOF, until Stop/Terminate/Destroy is called on the owning
// instance (matching a real hung process only being unblocked by an
// external kill).
type TurnScript struct {
	Events []subagent.ChildEvent
	Hang   bool
}

// startTurnCall records one FakeRuntime.StartTurn invocation.
type startTurnCall struct {
	Name string
	Spec TurnSpec
}

// fakeInstance is FakeRuntime's per-instance state.
type fakeInstance struct {
	state      string // "running" | "stopped"
	memCurrent uint64
	// turnWriter is set while a scripted turn's pipe hasn't reached EOF yet
	// (in particular, always set for a Hang script) — Stop/Terminate/Destroy
	// close it to simulate the process actually being killed.
	turnWriter *io.PipeWriter
}

// recordingWriteCloser captures everything written to a fake Turn's stdin
// (e.g. an approval_response Core sends back) so a test can assert on it —
// a real Runtime's Turn.SendApprovalSafe writes to a real process's stdin;
// this is the fake's equivalent capture point.
type recordingWriteCloser struct {
	mu  sync.Mutex
	buf []byte
}

func (w *recordingWriteCloser) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	return len(p), nil
}
func (w *recordingWriteCloser) Close() error { return nil }
func (w *recordingWriteCloser) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.buf)
}

// FakeRuntime is an in-memory Runtime test double — no real
// nspawn/systemd/subprocess involved. Supports scripted per-turn event
// streams and injectable failure modes (boot failure via StartErr, a
// hung turn via TurnScript.Hang, etc.) so Core's actor-loop/dispatch logic
// can be driven through every edge case without ever touching a real
// container. See docs/orchestrator-plan.md Step 18.
type FakeRuntime struct {
	mu        sync.Mutex
	instances map[string]*fakeInstance
	scripts   map[string][]TurnScript
	stdins    map[string]*recordingWriteCloser // instance name -> most recent turn's stdin recorder

	// CreateErr/StartErr, when set, make every Create/Start fail —
	// simulates a boot failure or a disk-full precheck failure.
	CreateErr error
	StartErr  error

	// stateRoot, when set (via WithStateRoot), makes Create also build the
	// real on-disk instance layout via CreateInstanceLayout — mirroring
	// nspawn.Runtime.Create's documented contract ("generates its
	// per-instance secrets/config"), needed by any test that exercises
	// SaveMeta against a real temp directory the way Core does.
	stateRoot string

	startTurnCalls []startTurnCall
}

// NewFakeRuntime returns an empty FakeRuntime.
func NewFakeRuntime() *FakeRuntime {
	return &FakeRuntime{
		instances: make(map[string]*fakeInstance),
		scripts:   make(map[string][]TurnScript),
		stdins:    make(map[string]*recordingWriteCloser),
	}
}

// WithStateRoot configures Create to also build each instance's real
// on-disk state-directory layout (secrets/, work/) via
// CreateInstanceLayout — see stateRoot's own doc comment. Returns the same
// *FakeRuntime for chaining, e.g. NewFakeRuntime().WithStateRoot(dir).
func (f *FakeRuntime) WithStateRoot(stateRoot string) *FakeRuntime {
	f.mu.Lock()
	f.stateRoot = stateRoot
	f.mu.Unlock()
	return f
}

// StartTurnCallCount returns how many times StartTurn has been called so
// far — thread-safe (unlike reading a public slice field directly would
// be, since StartTurn appends to it from the instance's own actor
// goroutine while a test polls concurrently).
func (f *FakeRuntime) StartTurnCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.startTurnCalls)
}

// StartTurnCallAt returns the i'th StartTurn call (0-indexed), thread-safe.
func (f *FakeRuntime) StartTurnCallAt(i int) startTurnCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.startTurnCalls[i]
}

// ScriptTurn queues one scripted turn's event sequence for the next
// StartTurn call against name — consumed FIFO, one script per call. A name
// with no queued script left gets a single synthetic "done" event
// (success:true) instead, so a test that doesn't care about a specific
// turn's content still gets a well-formed one.
func (f *FakeRuntime) ScriptTurn(name string, script TurnScript) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scripts[name] = append(f.scripts[name], script)
}

// StdinWritten returns everything Core wrote to the most recent turn's
// stdin for instance name (e.g. an approval_response) — "" if no turn has
// run yet.
func (f *FakeRuntime) StdinWritten(name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if w, ok := f.stdins[name]; ok {
		return w.String()
	}
	return ""
}

func (f *FakeRuntime) Create(_ context.Context, spec InstanceSpec) error {
	f.mu.Lock()
	if f.CreateErr != nil {
		f.mu.Unlock()
		return f.CreateErr
	}
	if _, ok := f.instances[spec.Name]; ok {
		f.mu.Unlock()
		return fmt.Errorf("fakeRuntime: instance %q already exists", spec.Name)
	}
	f.instances[spec.Name] = &fakeInstance{state: "stopped"}
	stateRoot := f.stateRoot
	f.mu.Unlock()

	if stateRoot != "" {
		if err := CreateInstanceLayout(stateRoot, spec.Name); err != nil {
			f.mu.Lock()
			delete(f.instances, spec.Name)
			f.mu.Unlock()
			return fmt.Errorf("fakeRuntime: create instance layout: %w", err)
		}
	}
	return nil
}

func (f *FakeRuntime) getOrCreate(name string) *fakeInstance {
	inst, ok := f.instances[name]
	if !ok {
		inst = &fakeInstance{}
		f.instances[name] = inst
	}
	return inst
}

func (f *FakeRuntime) Start(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.StartErr != nil {
		return f.StartErr
	}
	f.getOrCreate(name).state = "running"
	return nil
}

// stopLocked is Stop/Terminate's shared body: mark stopped and release any
// in-flight turn's pipe (simulating the process actually dying).
func (f *FakeRuntime) stopLocked(name string) {
	inst := f.getOrCreate(name)
	inst.state = "stopped"
	w := inst.turnWriter
	inst.turnWriter = nil
	if w != nil {
		w.Close()
	}
}

func (f *FakeRuntime) Stop(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopLocked(name)
	return nil
}

func (f *FakeRuntime) Terminate(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopLocked(name)
	return nil
}

func (f *FakeRuntime) Destroy(_ context.Context, name string) error {
	f.mu.Lock()
	f.stopLocked(name)
	delete(f.instances, name)
	delete(f.stdins, name)
	stateRoot := f.stateRoot
	f.mu.Unlock()

	if stateRoot != "" {
		_ = ShredSecrets(stateRoot, name)
		_ = os.RemoveAll(InstanceStateDir(stateRoot, name))
	}
	return nil
}

func (f *FakeRuntime) List(_ context.Context) ([]InstanceInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]InstanceInfo, 0, len(f.instances))
	for name, inst := range f.instances {
		out = append(out, InstanceInfo{Name: name, State: inst.state, MemoryCurrent: inst.memCurrent})
	}
	return out, nil
}

func (f *FakeRuntime) Exec(_ context.Context, _ string, _ []string) (string, string, int, error) {
	return "", "", 0, nil
}

func (f *FakeRuntime) StartTurn(ctx context.Context, name string, spec TurnSpec) (*Turn, error) {
	f.mu.Lock()
	f.startTurnCalls = append(f.startTurnCalls, startTurnCall{Name: name, Spec: spec})
	var script TurnScript
	if q := f.scripts[name]; len(q) > 0 {
		script = q[0]
		f.scripts[name] = q[1:]
	} else {
		script = TurnScript{Events: []subagent.ChildEvent{{Type: "done", Success: true}}}
	}
	stdin := &recordingWriteCloser{}
	f.stdins[name] = stdin
	inst := f.getOrCreate(name)
	f.mu.Unlock()

	pr, pw := io.Pipe()
	if script.Hang {
		f.mu.Lock()
		inst.turnWriter = pw
		f.mu.Unlock()
		// Hang means "never reaches EOF on its own", not "writes nothing"
		// — Events (if any) are written first (e.g. a single
		// approval_request a real turn would then block on), then the pipe
		// stays open until Stop/Terminate/Destroy closes it (an external
		// kill of a stuck process), or ctx is cancelled: the real nspawn
		// Runtime ties the local systemd-run client's lifetime to ctx via
		// exec.CommandContext, so cancelling it kills that local process
		// and unblocks ReadEvent the same way — this mirrors that so
		// Core's shutdown path (which cancels ctx) behaves identically
		// against the fake.
		go func() {
			for _, ev := range script.Events {
				data, err := json.Marshal(ev)
				if err != nil {
					continue
				}
				if _, err := pw.Write(append(data, '\n')); err != nil {
					return
				}
			}
			<-ctx.Done()
			pw.Close()
		}()
	} else {
		go func() {
			defer pw.Close()
			for _, ev := range script.Events {
				data, err := json.Marshal(ev)
				if err != nil {
					continue
				}
				if _, err := pw.Write(append(data, '\n')); err != nil {
					return
				}
			}
		}()
	}

	return &Turn{
		ChildProcess: subagent.AttachChild(stdin, pr),
		UnitName:     "fake-turn-" + name,
	}, nil
}

// StopTurn releases the named turn's pipe (same effect Stop/Terminate have
// on a hung turn) without marking the whole instance stopped — the fake's
// equivalent of `systemctl stop <unitName>`. unitName is always
// "fake-turn-<instance name>" here (see StartTurn), so the instance is
// recovered by stripping that prefix.
func (f *FakeRuntime) StopTurn(_ context.Context, unitName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := strings.TrimPrefix(unitName, "fake-turn-")
	if inst, ok := f.instances[name]; ok && inst.turnWriter != nil {
		inst.turnWriter.Close()
		inst.turnWriter = nil
	}
	return nil
}

// StopOrphanedTurn is the fake's equivalent of StopTurn's naming
// convention, for the restart-reconciliation path.
func (f *FakeRuntime) StopOrphanedTurn(ctx context.Context, name string) error {
	return f.StopTurn(ctx, "fake-turn-"+name)
}

var _ Runtime = (*FakeRuntime)(nil)
