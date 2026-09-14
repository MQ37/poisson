package orchestrator

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
)

// hostConfirmFlag is the exact, literal token /new-host requires on top of
// the config-level AllowHostInstances gate — required every single time,
// no pending-confirmation registry or timeout (see handleNew's own doc
// comment and docs/orchestrator-host-mode-plan.md §3.5).
const hostConfirmFlag = "--confirm-unconfined-host"

// stripHostConfirmFlag removes hostConfirmFlag from args (wherever it
// appears) and reports whether it was present — applied unconditionally
// (harmless for a box command, which never has a legitimate reason to
// contain this exact literal token).
func stripHostConfirmFlag(args []string) (filtered []string, confirmed bool) {
	for _, a := range args {
		if a == hostConfirmFlag {
			confirmed = true
			continue
		}
		filtered = append(filtered, a)
	}
	return filtered, confirmed
}

// countInstancesByKind returns how many currently-registered instances
// belong to kind — KindBox counts empty Kind too (see IsBoxKind), KindHost
// counts only an exact "host" match.
func (c *Core) countInstancesByKind(kind string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, inst := range c.byName {
		if kind == KindBox && IsBoxKind(inst.Meta.Kind) {
			n++
		} else if kind == KindHost && inst.Meta.Kind == KindHost {
			n++
		}
	}
	return n
}

// instanceNamesByKind is instanceNames filtered to one kind — for a
// refusal message that names what's actually running under the ceiling
// that was hit, not every instance regardless of kind.
func (c *Core) instanceNamesByKind(kind string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var names []string
	for name, inst := range c.byName {
		if (kind == KindBox && IsBoxKind(inst.Meta.Kind)) || (kind == KindHost && inst.Meta.Kind == KindHost) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// handleNew creates a brand-new instance: a fresh channel (e.g. a Telegram
// forum topic), the underlying Runtime instance, and its registry entry —
// in that order, so a failure partway through never leaves an instance
// nobody can reach (a channel with no instance behind it is just closed
// again; an instance the channel-creation step never got to is never
// created at all).
//
// kind is "box" (nspawn, /new and /new-box) or "host" (/new-host, runs
// directly on the orchestrator host with no isolation, real approvals,
// never yolo — see docs/orchestrator-host-mode-plan.md). Host instances
// take two friction checks alone, before anything else: the config-level
// AllowHostInstances gate, then an explicit confirmation flag required on
// EVERY invocation (no pending-confirmation state, no timeout — resolved
// entirely by what's actually typed back, mirroring /approve's "resolve
// via what's actually sent" simplicity).
func (c *Core) handleNew(ctx context.Context, cmd Command, kind string) {
	if kind == KindHost && !c.cfg.AllowHostInstances {
		c.safeSend(ctx, cmd.Key, Message{Kind: MsgError, Text: "host instances are disabled on this orchestrator — see [orchestrator] allow_host_instances"})
		return
	}
	args, confirmed := stripHostConfirmFlag(cmd.Args)
	if kind == KindHost && !confirmed {
		nameArg := "<name>"
		if len(args) > 0 {
			nameArg = args[0]
		}
		c.safeSend(ctx, cmd.Key, Message{Kind: MsgError, Text: fmt.Sprintf(
			"⚠️ a host instance runs directly on the orchestrator host with NO isolation: real host root, "+
				"the host's own credentials, every command it runs is a real command on this machine. "+
				"To confirm, resend the exact command: /new-host %s %s", nameArg, hostConfirmFlag)})
		return
	}

	if kind == KindBox {
		if count := c.countInstancesByKind(KindBox); count >= c.cfg.MaxInstances {
			c.safeSend(ctx, cmd.Key, Message{Kind: MsgError, Text: fmt.Sprintf(
				"already at the configured limit of %d instances: %s", c.cfg.MaxInstances, strings.Join(c.instanceNamesByKind(KindBox), ", "))})
			return
		}
	} else if c.cfg.MaxHostInstances > 0 {
		if count := c.countInstancesByKind(KindHost); count >= c.cfg.MaxHostInstances {
			c.safeSend(ctx, cmd.Key, Message{Kind: MsgError, Text: fmt.Sprintf(
				"already at the configured limit of %d host instances: %s", c.cfg.MaxHostInstances, strings.Join(c.instanceNamesByKind(KindHost), ", "))})
			return
		}
	}
	if c.cfg.DefaultProvider == "" || c.cfg.DefaultModel == "" {
		c.safeSend(ctx, cmd.Key, Message{Kind: MsgError, Text: "no default_model configured for the orchestrator — refusing to create an instance with no model"})
		return
	}

	requestedName, repoURL := "", ""
	if len(args) > 0 {
		requestedName = args[0]
	}
	if len(args) > 1 {
		repoURL = args[1]
	}

	name, err := ResolveInstanceName(requestedName)
	if err != nil {
		c.safeSend(ctx, cmd.Key, Message{Kind: MsgError, Text: fmt.Sprintf("could not resolve an instance name: %v", err)})
		return
	}

	key, err := c.fe.CreateChannel(ctx, name)
	if err != nil {
		c.safeSend(ctx, cmd.Key, Message{Kind: MsgError, Text: fmt.Sprintf("could not create a channel for %s: %v", name, err)})
		return
	}

	spec := InstanceSpec{
		Name:                name,
		Provider:            c.cfg.DefaultProvider,
		Model:               c.cfg.DefaultModel,
		AuthorizedProviders: []string{c.cfg.DefaultProvider},
	}
	// CompositeRuntime is kind-aware (CreateKind also checks cross-kind
	// name collisions); a bare Runtime (FakeRuntime/nspawn.Runtime
	// directly, as most tests and any non-host-aware caller use) has no
	// concept of kind at all, so it only ever gets the plain Create —
	// correct as long as such a caller only ever asks for "box" (the
	// backend IS the box runtime by construction there). This is the one
	// place in Core that has to know CompositeRuntime exists — a
	// deliberate, narrow exception to "Core only calls Runtime interface
	// methods" (see docs/orchestrator-host-mode-plan.md §3.3).
	if cr, ok := c.rt.(*CompositeRuntime); ok {
		err = cr.CreateKind(ctx, kind, spec)
	} else {
		err = c.rt.Create(ctx, spec)
	}
	if err != nil {
		_ = c.fe.CloseChannel(ctx, key)
		c.safeSend(ctx, cmd.Key, Message{Kind: MsgError, Text: fmt.Sprintf("failed to create instance %s: %v", name, err)})
		return
	}
	if err := c.rt.Start(ctx, name); err != nil {
		_ = c.rt.Destroy(ctx, name)
		_ = c.fe.CloseChannel(ctx, key)
		c.safeSend(ctx, cmd.Key, Message{Kind: MsgError, Text: fmt.Sprintf("instance %s created but failed to start: %v", name, err)})
		return
	}

	meta := InstanceMeta{
		Name:         name,
		SessionID:    "s-" + name,
		Model:        c.cfg.DefaultProvider + "/" + c.cfg.DefaultModel,
		Frontend:     key.Frontend,
		ChatID:       key.Chat,
		TopicID:      key.Topic,
		CreatedAt:    time.Now(),
		DesiredState: DesiredRunning,
		RepoURL:      repoURL,
		Kind:         kind,
	}
	if err := SaveMeta(c.cfg.StateRoot, meta); err != nil {
		// The instance is alive and usable regardless — a metadata save
		// failure only makes Step 21's reconciliation poorer for it after a
		// future restart, never a reason to tear down what's already
		// running.
		log.Printf("orchestrator: failed to save metadata for new instance %s: %v", name, err)
	}

	inst := NewInstance(meta, key, c.cfg.MailboxSize)
	c.registerInstance(ctx, inst)
	c.safeSend(ctx, key, Message{Kind: MsgLifecycle, Text: fmt.Sprintf("instance %s created and running", name)})

	if repoURL != "" {
		go c.cloneRepoIntoInstance(ctx, inst, repoURL)
	}
}

// cloneRepoIntoInstance clones repoURL into /work/repo as a first exec step
// *inside* the already-created (and already-usable) instance — never part
// of instance creation itself, so a clone failure leaves a live, working
// instance with an empty /work plus an explanatory message, never a
// half-destroyed instance.
func (c *Core) cloneRepoIntoInstance(ctx context.Context, inst *Instance, repoURL string) {
	_, stderr, code, err := c.rt.Exec(ctx, inst.Meta.Name, []string{"git", "clone", repoURL, "/work/repo"})
	if err != nil || code != 0 {
		c.safeSend(ctx, inst.Key, Message{Kind: MsgError, Text: fmt.Sprintf(
			"repo clone failed (instance is still live, /work is empty): %v %s", err, strings.TrimSpace(stderr))})
		return
	}
	c.safeSend(ctx, inst.Key, Message{Kind: MsgLifecycle, Text: "repo cloned into /work/repo"})
}

// handleList replies with every instance's name, status, and model.
func (c *Core) handleList(ctx context.Context, cmd Command) {
	c.mu.Lock()
	lines := make([]string, 0, len(c.byName))
	for name, inst := range c.byName {
		lines = append(lines, fmt.Sprintf("%s: %s (%s)", name, inst.Status(), inst.ModelString()))
	}
	c.mu.Unlock()
	if len(lines) == 0 {
		c.safeSend(ctx, cmd.Key, Message{Kind: MsgText, Text: "no instances running"})
		return
	}
	sort.Strings(lines)
	c.safeSend(ctx, cmd.Key, Message{Kind: MsgText, Text: strings.Join(lines, "\n")})
}

// handleInstanceCommand dispatches every per-instance command besides
// CmdMessage (which goes through the mailbox instead — see route).
func (c *Core) handleInstanceCommand(ctx context.Context, inst *Instance, cmd Command) {
	switch cmd.Kind {
	case CmdStatus:
		c.handleStatus(ctx, inst)
	case CmdModel:
		c.handleModel(ctx, inst, cmd)
	case CmdSuspend:
		c.handleSuspend(ctx, inst)
	case CmdResume:
		c.handleResume(ctx, inst)
	case CmdKill:
		c.handleKill(ctx, inst)
	case CmdApprove:
		c.handleApproval(ctx, inst, true, "")
	case CmdDeny:
		c.handleApproval(ctx, inst, false, strings.Join(cmd.Args, " "))
	case CmdCancel:
		c.handleCancel(ctx, inst)
	case CmdHelp:
		c.safeSend(ctx, inst.Key, Message{Kind: MsgText, Text: helpText()})
	default:
		c.safeSend(ctx, inst.Key, Message{Kind: MsgError, Text: "unrecognized command"})
	}
}

// statusExecTimeout bounds each Runtime.Exec call /status makes inside the
// target instance — a slow or hanging exec must not make the whole /status
// command appear to hang; a timeout still yields a partial (labeled) report
// instead of nothing.
const statusExecTimeout = 10 * time.Second

// statusReplyCap bounds /status's combined reply. Telegram's own 4096-char
// limit is already handled gracefully by the client's chunking (a long
// message still sends, just as multiple messages) — this cap is a
// deliberate, separate ceiling so a single /status reply (e.g. `px
// sessions` output growing unbounded over an instance's lifetime) doesn't
// flood the topic with many chunks for what's meant to be a quick check.
const statusReplyCap = 3500

// handleStatus reports an instance's live state: desired/actual status,
// current model, queue depth, any pending approval, and — reusing `px
// cost`/`px sessions` verbatim via Runtime.Exec rather than writing any new
// cost-reporting logic (see docs/orchestrator-plan.md §4 reuse decision 6)
// — its cost/session summary. A suspended instance reports that directly
// instead of attempting an Exec that would just fail confusingly.
func (c *Core) handleStatus(ctx context.Context, inst *Instance) {
	meta := inst.MetaSnapshot()
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s (%s)\n", meta.Name, inst.Status(), inst.ModelString())
	fmt.Fprintf(&b, "queue depth: %d\n", len(inst.mailbox))

	if info, ok := c.instanceInfo(ctx, meta.Name); ok {
		if !info.Since.IsZero() {
			fmt.Fprintf(&b, "up since: %s\n", info.Since.Format(time.RFC3339))
		}
		if info.MemoryCurrent > 0 {
			fmt.Fprintf(&b, "memory: %s\n", formatBytes(info.MemoryCurrent))
		}
	}

	switch {
	case meta.DesiredState == DesiredSuspended:
		b.WriteString("suspended — no cost/session data available while stopped\n")
	default:
		execCtx, cancel := context.WithTimeout(ctx, statusExecTimeout)
		stdout, stderr, code, err := c.rt.Exec(execCtx, meta.Name, []string{"px", "cost", meta.SessionID})
		cancel()
		switch {
		case err != nil:
			fmt.Fprintf(&b, "cost: exec failed: %v\n", err)
		case code != 0:
			fmt.Fprintf(&b, "cost: exec exited %d: %s\n", code, strings.TrimSpace(stderr))
		default:
			fmt.Fprintf(&b, "%s\n", strings.TrimSpace(stdout))
		}

		execCtx, cancel = context.WithTimeout(ctx, statusExecTimeout)
		stdout, _, code, err = c.rt.Exec(execCtx, meta.Name, []string{"px", "sessions"})
		cancel()
		if err == nil && code == 0 {
			fmt.Fprintf(&b, "sessions:\n%s\n", strings.TrimSpace(stdout))
		}
	}

	if p := inst.Pending(); p != nil {
		fmt.Fprintf(&b, "awaiting approval (risk %s): %s\n", p.Risk, p.Command)
	}

	text := b.String()
	if len(text) > statusReplyCap {
		text = text[:statusReplyCap] + "\n...[truncated]"
	}
	c.safeSend(ctx, inst.Key, Message{Kind: MsgText, Text: text})
}

// instanceInfo looks up name's live entry from Runtime.List — the backend's
// own report, not any in-memory cache (see docs/orchestrator-plan.md §4
// reuse decision 4).
func (c *Core) instanceInfo(ctx context.Context, name string) (InstanceInfo, bool) {
	infos, err := c.rt.List(ctx)
	if err != nil {
		return InstanceInfo{}, false
	}
	for _, info := range infos {
		if info.Name == name {
			return info, true
		}
	}
	return InstanceInfo{}, false
}

// formatBytes renders a byte count in the largest whole unit that keeps it
// readable (e.g. "512.3 MiB"), for /status's memory line.
func formatBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// handleModel validates the requested model against the allow-list and
// updates the instance's metadata — the change is written into
// inst.Meta.Model and passed as --model on the NEXT `px -p` invocation
// (runPrint's existing single-UPDATE path), never mid-turn.
func (c *Core) handleModel(ctx context.Context, inst *Instance, cmd Command) {
	if len(cmd.Args) == 0 {
		c.safeSend(ctx, inst.Key, Message{Kind: MsgError, Text: fmt.Sprintf(
			"usage: /model <provider/model>. allowed: %s", strings.Join(c.cfg.AllowedModels, ", "))})
		return
	}
	requested := cmd.Args[0]
	if !c.cfg.modelAllowed(requested) {
		c.safeSend(ctx, inst.Key, Message{Kind: MsgError, Text: fmt.Sprintf(
			"model %q is not on the allow-list. allowed: %s", requested, strings.Join(c.cfg.AllowedModels, ", "))})
		return
	}
	inst.SetModel(requested)
	if err := SaveMeta(c.cfg.StateRoot, inst.MetaSnapshot()); err != nil {
		log.Printf("orchestrator: failed to persist model change for %s: %v", inst.Meta.Name, err)
	}
	c.safeSend(ctx, inst.Key, Message{Kind: MsgLifecycle, Text: fmt.Sprintf(
		"model set to %s — takes effect on the NEXT turn, not the current one", requested)})
}

// stopCurrentTurn stops inst's in-flight turn (if any), marking it as a
// deliberate stop so pumpTurn's "ended with no done event" branch reports
// "turn stopped" rather than "crashed". Best-effort and safe to call when
// no turn is running (nothing to do).
func (c *Core) stopCurrentTurn(ctx context.Context, inst *Instance) {
	turn := inst.CurrentTurn()
	if turn == nil {
		return
	}
	inst.markTurnKilledByUs()
	if err := c.rt.StopTurn(ctx, turn.UnitName); err != nil {
		log.Printf("orchestrator: StopTurn(%s) failed: %v", turn.UnitName, err)
	}
}

// handleSuspend stops the current turn (if any), then powers the instance
// off (rootfs kept — DesiredState becomes suspended, the mechanism /resume
// reverses).
func (c *Core) handleSuspend(ctx context.Context, inst *Instance) {
	c.stopCurrentTurn(ctx, inst)
	if err := c.rt.Stop(ctx, inst.Meta.Name); err != nil {
		c.safeSend(ctx, inst.Key, Message{Kind: MsgError, Text: fmt.Sprintf("failed to suspend: %v", err)})
		return
	}
	inst.SetDesiredState(DesiredSuspended)
	if err := SaveMeta(c.cfg.StateRoot, inst.MetaSnapshot()); err != nil {
		log.Printf("orchestrator: failed to persist suspend for %s: %v", inst.Meta.Name, err)
	}
	c.safeSend(ctx, inst.Key, Message{Kind: MsgLifecycle, Text: "suspended"})
}

// handleResume starts a suspended instance back up. A never-suspended (or
// already-killed/unregistered) instance gets a specific, clear error —
// never a generic failure.
func (c *Core) handleResume(ctx context.Context, inst *Instance) {
	if inst.DesiredStateValue() != DesiredSuspended {
		c.safeSend(ctx, inst.Key, Message{Kind: MsgError, Text: "this instance isn't suspended — nothing to resume"})
		return
	}
	if err := c.rt.Start(ctx, inst.Meta.Name); err != nil {
		c.safeSend(ctx, inst.Key, Message{Kind: MsgError, Text: fmt.Sprintf("failed to resume: %v", err)})
		return
	}
	inst.SetDesiredState(DesiredRunning)
	if err := SaveMeta(c.cfg.StateRoot, inst.MetaSnapshot()); err != nil {
		log.Printf("orchestrator: failed to persist resume for %s: %v", inst.Meta.Name, err)
	}
	c.safeSend(ctx, inst.Key, Message{Kind: MsgLifecycle, Text: "resumed"})
}

// handleKill permanently destroys an instance, in the exact order the plan
// requires so a crash at any point leaves something Step 21's
// reconciliation can finish cleanly on the next startup: mark
// PendingDestroy first, stop the turn, power off (falling back to
// Terminate), remove the rootfs/secrets, close the channel, and only then
// drop the registry entry.
func (c *Core) handleKill(ctx context.Context, inst *Instance) {
	inst.SetPendingDestroy(true)
	if err := SaveMeta(c.cfg.StateRoot, inst.MetaSnapshot()); err != nil {
		log.Printf("orchestrator: failed to persist PendingDestroy for %s: %v", inst.Meta.Name, err)
	}

	c.stopCurrentTurn(ctx, inst)

	stopCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	stopErr := c.rt.Stop(stopCtx, inst.Meta.Name)
	cancel()
	if stopErr != nil {
		if err := c.rt.Terminate(ctx, inst.Meta.Name); err != nil {
			log.Printf("orchestrator: Terminate(%s) during /kill failed: %v", inst.Meta.Name, err)
		}
	}

	if err := c.rt.Destroy(ctx, inst.Meta.Name); err != nil {
		c.safeSend(ctx, inst.Key, Message{Kind: MsgError, Text: fmt.Sprintf("failed to fully destroy %s: %v (metadata still marked pending-destroy; will retry on next restart)", inst.Meta.Name, err)})
		return
	}

	if err := c.fe.CloseChannel(ctx, inst.Key); err != nil {
		log.Printf("orchestrator: CloseChannel for %s failed (non-fatal): %v", inst.Meta.Name, err)
	}

	c.unregisterInstance(inst.Meta.Name)
}

// handleApproval resolves inst's currently pending approval, if any.
func (c *Core) handleApproval(ctx context.Context, inst *Instance, approved bool, reason string) {
	turn := inst.CurrentTurn()
	if turn == nil || inst.Pending() == nil {
		c.safeSend(ctx, inst.Key, Message{Kind: MsgError, Text: "no approval is currently pending on this instance"})
		return
	}
	if err := turn.SendApprovalSafe(approved, reason); err != nil {
		c.safeSend(ctx, inst.Key, Message{Kind: MsgError, Text: fmt.Sprintf("failed to send approval response: %v", err)})
		return
	}
	inst.setPending(nil)
	inst.setStatus(StatusRunning)
}

// handleCancel stops the current turn (if any) — the user-facing "give up
// on this turn" command, distinct from /kill (which also destroys the
// instance).
func (c *Core) handleCancel(ctx context.Context, inst *Instance) {
	if inst.CurrentTurn() == nil {
		c.safeSend(ctx, inst.Key, Message{Kind: MsgError, Text: "no turn is currently running on this instance"})
		return
	}
	c.stopCurrentTurn(ctx, inst)
	c.safeSend(ctx, inst.Key, Message{Kind: MsgLifecycle, Text: "cancelling current turn…"})
}

// helpText is /help's static reply.
func helpText() string {
	return `Commands:
/new [name] [repo-url]        create a new box instance (alias for /new-box)
/new-box [name] [repo-url]    create a new box (isolated, yolo) instance
/new-host [name] --confirm-unconfined-host
                               create a host-direct (unconfined, real approvals) instance
/list                    list every instance
/status                  this instance's status
/model <provider/model>  switch this instance's model (next turn)
/suspend                 power off, keep rootfs (resumable)
/resume                  power a suspended instance back on
/kill                    permanently destroy this instance
/approve                 approve the pending bash command
/deny [reason]           deny the pending bash command
/cancel                  stop the current turn
/help                    this message`
}
