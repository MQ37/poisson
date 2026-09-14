package orchestrator

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// bootIDFile records which boot last had its reconciliation notice posted
// — a Restart=always crash loop (docs/orchestrator-plan.md Step 27) calls
// reconcile() on every single restart attempt, but the "orchestrator
// restarted" notice should fire at most once per actual host boot, not
// once per crash-loop iteration.
const bootIDFile = "last_boot_id"

// currentBootID reads the kernel's boot id — a UUID that changes every
// boot. Empty (e.g. no /proc, non-Linux, a sandboxed environment) means
// "can't tell", which shouldNotifyForBoot treats as "notify anyway" —
// safer than silently going quiet forever if this file is ever
// unreadable.
func currentBootID() string {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// shouldNotifyForBoot reports whether this run should post reconciliation
// notices, and records the current boot id so a subsequent call within the
// same boot (e.g. a crash-loop restart) returns false.
func shouldNotifyForBoot(stateRoot string) bool {
	current := currentBootID()
	if current == "" {
		return true
	}
	path := filepath.Join(stateRoot, bootIDFile)
	prev, _ := os.ReadFile(path)
	if strings.TrimSpace(string(prev)) == current {
		return false
	}
	if err := WriteFileAtomic(path, []byte(current), 0o600); err != nil {
		log.Printf("orchestrator: failed to persist boot id: %v", err)
	}
	return true
}

// reconcile runs once at Core startup: scan the state directory (Step 14's
// crash-safe metadata), call rt.List for ground truth, and reconcile the
// two — following the sandbox.Manager.EnableDiscovery pattern exactly (see
// docs/orchestrator-plan.md §4 reuse decision 4): the runtime's own List is
// truth, the state directory is a cache on top of it, never the reverse.
// This is also the direct answer to "what happens when the host reboots" —
// with Restart=always on the host-level unit, this runs automatically
// after every restart with no separate reboot-specific code.
func (c *Core) reconcile(ctx context.Context) {
	metas, issues, err := ScanInstances(c.cfg.StateRoot)
	if err != nil {
		log.Printf("orchestrator: reconciliation scan failed: %v", err)
		return
	}
	for _, issue := range issues {
		log.Printf("orchestrator: reconciliation: skipping instance %q with unreadable metadata: %v", issue.Name, issue.Err)
	}

	// Second (and only other) narrow, documented exception to "Core only
	// calls Runtime interface methods" — see RehydrateKindOf's own doc
	// comment for why this must run before anything below touches an
	// existing instance.
	if cr, ok := c.rt.(*CompositeRuntime); ok {
		cr.RehydrateKindOf(metas)
	}

	liveInfos, err := c.rt.List(ctx)
	if err != nil {
		log.Printf("orchestrator: reconciliation: rt.List failed: %v", err)
		liveInfos = nil
	}
	live := make(map[string]InstanceInfo, len(liveInfos))
	for _, info := range liveInfos {
		live[info.Name] = info
	}
	metaByName := make(map[string]bool, len(metas))
	notify := shouldNotifyForBoot(c.cfg.StateRoot)

	for _, meta := range metas {
		metaByName[meta.Name] = true
		c.reconcileOne(ctx, meta, live, notify)
	}

	// Rootfs found running but no metadata at all — report it as an
	// orphan, do not silently adopt it: there is no channel to route its
	// output to.
	for name := range live {
		if !metaByName[name] {
			log.Printf("orchestrator: reconciliation: found orphan instance %q with no metadata — not adopting it", name)
		}
	}
}

// reconcileOne reconciles a single instance's persisted metadata against
// the runtime's live report, in the order docs/orchestrator-plan.md Step 21
// specifies.
func (c *Core) reconcileOne(ctx context.Context, meta InstanceMeta, live map[string]InstanceInfo, notify bool) {
	// Presence in live alone isn't enough — Runtime.List reports a Created-
	// but-never-Started (or already-Stopped) instance too, just with
	// State != "running". Only an actual State=="running" counts as
	// running for the switch below.
	info, found := live[meta.Name]
	running := found && info.State == "running"

	// A previous /kill was interrupted mid-flight — finish it rather than
	// leaving the instance half-removed. Never registered afterward: it's
	// gone.
	if meta.PendingDestroy {
		if err := c.rt.Destroy(ctx, meta.Name); err != nil {
			log.Printf("orchestrator: reconciliation: failed to finish destroying %q: %v", meta.Name, err)
			return
		}
		log.Printf("orchestrator: reconciliation: finished an interrupted /kill for %q", meta.Name)
		return
	}

	// A turn unit is a transient HOST-systemd unit, not something the
	// orchestrator process itself owns in memory — it survives the
	// orchestrator's own death but can never be meaningfully re-attached to
	// a new process. Stop it (harmless no-op if there wasn't one); the
	// user's message that started it is already durably in the instance's
	// own session DB regardless of whether the turn finished, so the next
	// message still continues the same conversation coherently.
	if err := c.rt.StopOrphanedTurn(ctx, meta.Name); err != nil {
		log.Printf("orchestrator: reconciliation: StopOrphanedTurn(%q) failed: %v", meta.Name, err)
	}

	switch {
	case !running && meta.DesiredState == DesiredRunning:
		// Metadata says it should exist but the machine is missing
		// entirely (e.g. host rebooted before it was ever actually
		// started) — start it fresh.
		if err := c.rt.Start(ctx, meta.Name); err != nil {
			log.Printf("orchestrator: reconciliation: failed to start %q: %v", meta.Name, err)
		}
	case running && meta.DesiredState == DesiredSuspended:
		if err := c.rt.Stop(ctx, meta.Name); err != nil {
			log.Printf("orchestrator: reconciliation: failed to stop suspended %q: %v", meta.Name, err)
		}
	}

	key := ChannelKey{Frontend: meta.Frontend, Chat: meta.ChatID, Topic: meta.TopicID}
	inst := NewInstance(meta, key, c.cfg.MailboxSize)
	c.registerInstance(ctx, inst)

	// Rate-limited to once per actual boot (see shouldNotifyForBoot) — a
	// Restart=always crash loop must not spam this notice on every single
	// restart attempt.
	if meta.DesiredState == DesiredRunning && notify {
		c.safeSend(ctx, key, Message{
			Kind: MsgLifecycle,
			Text: "orchestrator restarted — any previous turn was interrupted. Your conversation continues normally with your next message.",
		})
	}
}
