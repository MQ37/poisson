package orchestrator

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// CoreConfig is Core's own runtime tunables — deliberately separate from
// config.OrchestratorConfig (the on-disk TOML shape): this package must
// never import internal/config (that package has no reason to import
// orchestrator back, but keeping the dependency one-directional here costs
// nothing and avoids ever having to worry about it). cmd/px's own
// orchestrate.go (Step 26) translates one into the other.
type CoreConfig struct {
	MaxInstances       int
	MaxConcurrentTurns int
	MailboxSize        int
	DefaultProvider    string
	DefaultModel       string   // bare model name paired with DefaultProvider
	AllowedModels      []string // "provider/model" entries; empty means /model refuses everything
	StateRoot          string
	// AllowHostInstances gates /new-host entirely -- false (the default)
	// refuses the command outright, before even the confirmation-flag check
	// (see handleNew). A host instance has no isolation boundary at all —
	// this must be an explicit, conscious opt-in.
	AllowHostInstances bool
	// MaxHostInstances caps concurrent host-direct instances, independent
	// of MaxInstances (which counts box instances only). 0 (default) means
	// unlimited -- host instances are gated by AllowHostInstances plus the
	// per-invocation confirmation step instead of a count ceiling.
	MaxHostInstances int
}

func (c CoreConfig) withDefaults() CoreConfig {
	if c.MaxInstances <= 0 {
		c.MaxInstances = 5
	}
	if c.MaxConcurrentTurns <= 0 {
		c.MaxConcurrentTurns = 3
	}
	if c.MailboxSize <= 0 {
		c.MailboxSize = 8
	}
	return c
}

// modelAllowed reports whether "provider/model" s is on the allow-list —
// same "empty means refuse everything" contract as
// config.Config.OrchestratorModelAllowed, duplicated here in plain-slice
// form so Core doesn't need to import internal/config for one lookup.
func (c CoreConfig) modelAllowed(s string) bool {
	for _, m := range c.AllowedModels {
		if m == s {
			return true
		}
	}
	return false
}

// Core is the orchestrator's central registry and router: it owns every
// live Instance, routes incoming Commands to the right one's mailbox (or
// handles instance-less commands directly), and never runs a turn itself —
// each instance's own actor goroutine does that. See
// docs/orchestrator-plan.md Step 19.
type Core struct {
	mu     sync.Mutex
	byKey  map[ChannelKey]*Instance
	byName map[string]*Instance

	rt  Runtime
	fe  Frontend
	cfg CoreConfig

	// turnSem caps how many turns run concurrently across ALL instances —
	// separate from MaxInstances (how many instances merely exist), per
	// docs/orchestrator-plan.md §4 reuse decision 3's "two independent
	// caps".
	turnSem chan struct{}

	wg sync.WaitGroup
}

// NewCore returns a Core ready to Run. rt and fe are typically
// nspawn.Runtime/telegram.Frontend in production, FakeRuntime/FakeFrontend
// in tests.
func NewCore(rt Runtime, fe Frontend, cfg CoreConfig) *Core {
	cfg = cfg.withDefaults()
	return &Core{
		byKey:   make(map[ChannelKey]*Instance),
		byName:  make(map[string]*Instance),
		rt:      rt,
		fe:      fe,
		cfg:     cfg,
		turnSem: make(chan struct{}, cfg.MaxConcurrentTurns),
	}
}

// Run reconciles state against the runtime (Step 21), then starts the
// frontend's Run loop and dispatches every Command it produces until ctx is
// cancelled or the frontend itself returns. On ctx cancellation, every
// in-flight turn is interrupted and every pending approval force-denied
// before Run returns (see shutdown).
func (c *Core) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	c.reconcile(runCtx)

	cmds := make(chan Command, 32)
	feErrCh := make(chan error, 1)
	go func() {
		feErrCh <- c.fe.Run(runCtx, cmds)
	}()

	for {
		select {
		case <-runCtx.Done():
			c.shutdown()
			return runCtx.Err()
		case cmd := <-cmds:
			c.route(runCtx, cmd)
		case err := <-feErrCh:
			c.shutdown()
			return err
		}
	}
}

// shutdown cancels every registered instance's mailbox delivery (the
// per-instance actor goroutines already select on the Run ctx, which is
// already cancelled by the time this runs) and force-denies every pending
// approval, then waits briefly for actor goroutines to exit — matching
// docs/server-mode-plan.md's own shutdown design (approvals never left
// hanging across a planned restart).
func (c *Core) shutdown() {
	c.mu.Lock()
	instances := make([]*Instance, 0, len(c.byName))
	for _, inst := range c.byName {
		instances = append(instances, inst)
	}
	c.mu.Unlock()

	for _, inst := range instances {
		if turn := inst.CurrentTurn(); turn != nil {
			_ = turn.SendApprovalSafe(false, "orchestrator shutting down")
		}
	}

	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		log.Printf("orchestrator: shutdown timed out waiting for %d instance actor(s) to exit", len(instances))
	}
}

// safeSend delivers msg to key, logging (not panicking or blocking the
// caller) on failure — every call site in this package treats Send as
// best-effort, since a frontend outage must never take down turn
// processing itself.
func (c *Core) safeSend(ctx context.Context, key ChannelKey, msg Message) {
	if _, err := c.fe.Send(ctx, key, msg); err != nil {
		log.Printf("orchestrator: send to %+v failed: %v", key, err)
	}
}

// route dispatches one incoming Command: CmdNew/CmdList are instance-less
// (handled directly, see commands.go); everything else resolves its target
// instance from the Command's ChannelKey. CmdMessage is the one kind that
// goes through the instance's own mailbox (so two messages to the same
// instance serialize instead of racing); every other per-instance command
// acts immediately, bypassing the mailbox, specifically so it can interrupt
// an in-flight turn (e.g. /kill mid-turn) instead of queuing behind it.
func (c *Core) route(ctx context.Context, cmd Command) {
	switch cmd.Kind {
	case CmdNewBox: // == CmdNew, the bare /new alias
		c.handleNew(ctx, cmd, KindBox)
		return
	case CmdNewHost:
		c.handleNew(ctx, cmd, KindHost)
		return
	case CmdList:
		c.handleList(ctx, cmd)
		return
	}

	c.mu.Lock()
	inst, ok := c.byKey[cmd.Key]
	c.mu.Unlock()
	if !ok {
		c.safeSend(ctx, cmd.Key, Message{Kind: MsgError, Text: "no instance here — use /new in the general topic to create one"})
		return
	}

	if cmd.Kind == CmdMessage {
		c.enqueueMessage(ctx, inst, cmd)
		return
	}
	c.handleInstanceCommand(ctx, inst, cmd)
}

// enqueueMessage posts cmd onto inst's mailbox, or replies explicitly (never
// silently drops) if it's genuinely full — the class of bug commit
// 19723c6 ("drop queued messages on session switch instead of leaking them
// into the next one") already had to fix once in the TUI.
func (c *Core) enqueueMessage(ctx context.Context, inst *Instance, cmd Command) {
	select {
	case inst.mailbox <- cmd:
	default:
		c.safeSend(ctx, inst.Key, Message{Kind: MsgError, Text: "queue full, dropping this message — please wait for the current turn to finish"})
	}
}

// registerInstance adds inst to both registry maps and starts its actor
// goroutine, bound to runCtx (so it exits on shutdown along with every
// other instance).
func (c *Core) registerInstance(runCtx context.Context, inst *Instance) {
	c.mu.Lock()
	c.byKey[inst.Key] = inst
	c.byName[inst.Meta.Name] = inst
	c.mu.Unlock()

	c.wg.Add(1)
	go c.runActor(runCtx, inst)
}

// unregisterInstance removes inst from both registry maps — called once
// /kill has fully torn everything else down (see commands.go), never
// before, so a crash mid-teardown always finds the instance still
// registered and Step 21's reconciliation can finish the job.
func (c *Core) unregisterInstance(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	inst, ok := c.byName[name]
	if !ok {
		return
	}
	delete(c.byName, name)
	delete(c.byKey, inst.Key)
}

// runActor is one instance's entire lifetime: drain its mailbox serially,
// one CmdMessage (one turn) at a time, until ctx is cancelled or the
// mailbox is closed. A panic anywhere in a turn is recovered here, marking
// the instance dead and reporting it to its own topic — the same
// recoverChildPanic precedent used elsewhere in this codebase (see
// cmd/px/main.go), applied to a whole instance's actor instead of one
// subagent process.
func (c *Core) runActor(ctx context.Context, inst *Instance) {
	defer c.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			inst.setStatus(StatusDead)
			log.Printf("orchestrator: instance %s actor panicked: %v", inst.Meta.Name, r)
			c.safeSend(context.Background(), inst.Key, Message{
				Kind: MsgError,
				Text: fmt.Sprintf("instance %s crashed internally and is now dead — contact the operator", inst.Meta.Name),
			})
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case cmd, ok := <-inst.mailbox:
			if !ok {
				return
			}
			if inst.Status() == StatusDead {
				continue // a dead instance's mailbox just drains harmlessly from here on
			}
			c.runTurn(ctx, inst, cmd)
		}
	}
}
