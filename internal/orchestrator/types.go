package orchestrator

import (
	"sync"
	"time"

	"github.com/mq37/poisson/internal/subagent"
)

// ChannelKey is one frontend's opaque routing target for one instance: a
// Telegram message_thread_id, a future Discord thread id, an HTTP session
// path. Core never parses it — it is a map key and nothing more. A plain
// comparable struct (only string fields) so it works directly as a map key.
type ChannelKey struct {
	Frontend string // "telegram" today; "discord"/"http"/... later, same interface
	Chat     string
	Topic    string // "" for a frontend's general/instance-less context
}

// CommandKind enumerates every user-issued action Core dispatches (see
// commands.go).
type CommandKind int

const (
	CmdMessage CommandKind = iota // free text -> one agent turn
	CmdNewBox
	CmdList
	CmdStatus
	CmdModel
	CmdSuspend
	CmdResume
	CmdKill
	CmdApprove
	CmdDeny
	CmdCancel
	CmdHelp
	CmdNewHost
)

// CmdNew is a literal alias for CmdNewBox: bare /new (no suffix) keeps
// working exactly as before, for the Telegram parser and for anyone with
// muscle memory — see docs/orchestrator-host-mode-plan.md §3.5.
const CmdNew = CmdNewBox

// Command is one incoming user action, already parsed by whichever Frontend
// received it — transport-agnostic from here on; Core never sees a
// Telegram-shaped (or any other transport-shaped) type.
type Command struct {
	Key    ChannelKey
	Kind   CommandKind
	Text   string
	Args   []string
	UserID string
	At     time.Time
}

// Event mirrors agent.OutputEvent / subagent.ChildEvent deliberately — see
// docs/orchestrator-plan.md §4 reuse decision 1: subagent.ChildEvent is
// embedded as-is, a field copy, never a second translation layer.
// InstanceName/Seq/At are the only fields this package adds.
type Event struct {
	InstanceName string
	Seq          uint64
	At           time.Time
	subagent.ChildEvent
}

// Status is an instance's own lifecycle state — the four-state enum from
// docs/server-mode-plan.md, carried over per §4 reuse decision 3, plus
// StatusDead (this feature's own addition: an instance whose actor
// goroutine panicked — see Step 19).
type Status int

const (
	StatusIdle Status = iota
	StatusQueued
	StatusRunning
	StatusAwaitingApproval
	StatusDead
)

// String renders Status the way it appears in /status and /list replies.
func (s Status) String() string {
	switch s {
	case StatusIdle:
		return "idle"
	case StatusQueued:
		return "queued"
	case StatusRunning:
		return "running"
	case StatusAwaitingApproval:
		return "awaiting_approval"
	case StatusDead:
		return "dead"
	default:
		return "unknown"
	}
}

// PendingApproval is one outstanding approval_request the instance's
// current turn is blocked on.
type PendingApproval struct {
	Command     string
	Description string
	Risk        string
}

// Instance is one live (or suspended) agent instance's in-memory handle —
// Core's registry entries. Meta is the crash-safe persisted record (see
// state.go); everything else here is process-local bookkeeping that does
// not survive an orchestrator restart on its own (Step 21's reconciliation
// is what rebuilds it from Meta + Runtime.List after one).
//
// Meta's Model/DesiredState/PendingDestroy fields mutate after
// construction (via /model, /suspend, /resume, /kill) while the actor
// goroutine concurrently reads Meta.Model for the next turn — every access
// to those three fields (read or write) after construction goes through
// the accessor methods below, all guarded by mu, never a direct field
// read/write. Name/SessionID/ChatID/TopicID/CreatedAt/RepoURL are set once
// at construction and never mutated afterward, so reading them directly off
// Meta is safe.
type Instance struct {
	Meta InstanceMeta
	Key  ChannelKey

	mu      sync.Mutex
	status  Status
	pending *PendingApproval

	// currentTurn is the in-flight turn's handle, if any — set by the actor
	// goroutine (runTurn) while a turn runs, read by the dispatch loop
	// (handleInstanceCommand) so /kill, /suspend, /approve, /deny, /cancel
	// can act on it directly without going through the mailbox.
	currentTurn *Turn
	// turnKilledByUs is set immediately before this package itself stops an
	// in-flight turn (StopTurn/Terminate/Destroy via /kill, /suspend, or
	// /cancel) — read once (and reset) by pumpTurn if the turn then ends
	// with no "done" event, to distinguish "we stopped this on purpose"
	// from "it crashed" in the message reported to the topic.
	turnKilledByUs bool

	// mailbox is this instance's own bounded inbox — one goroutine per
	// instance drains it serially, so two messages to the same instance
	// never race each other (see Step 19).
	mailbox chan Command
}

// NewInstance builds an Instance in StatusIdle with a mailbox of the given
// capacity.
func NewInstance(meta InstanceMeta, key ChannelKey, mailboxSize int) *Instance {
	return &Instance{
		Meta:    meta,
		Key:     key,
		status:  StatusIdle,
		mailbox: make(chan Command, mailboxSize),
	}
}

// Status returns the instance's current status.
func (i *Instance) Status() Status {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.status
}

func (i *Instance) setStatus(s Status) {
	i.mu.Lock()
	i.status = s
	i.mu.Unlock()
}

// Pending returns the instance's current outstanding approval, or nil.
func (i *Instance) Pending() *PendingApproval {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.pending
}

func (i *Instance) setPending(p *PendingApproval) {
	i.mu.Lock()
	i.pending = p
	i.mu.Unlock()
}

// CurrentTurn returns the instance's in-flight turn handle, or nil.
func (i *Instance) CurrentTurn() *Turn {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.currentTurn
}

func (i *Instance) setCurrentTurn(t *Turn) {
	i.mu.Lock()
	i.currentTurn = t
	i.mu.Unlock()
}

// markTurnKilledByUs records that this package is about to deliberately
// stop the current turn — see turnKilledByUs's own doc comment.
func (i *Instance) markTurnKilledByUs() {
	i.mu.Lock()
	i.turnKilledByUs = true
	i.mu.Unlock()
}

// consumeTurnKilledByUs reads and resets turnKilledByUs in one step, so
// each turn's own flag never leaks into the next one.
func (i *Instance) consumeTurnKilledByUs() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	v := i.turnKilledByUs
	i.turnKilledByUs = false
	return v
}

// ModelString returns the instance's current "provider/model" pair —
// thread-safe against a concurrent /model command.
func (i *Instance) ModelString() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.Meta.Model
}

// SetModel updates the instance's model pair (see handleModel — takes
// effect on the next turn, not the current one).
func (i *Instance) SetModel(model string) {
	i.mu.Lock()
	i.Meta.Model = model
	i.mu.Unlock()
}

// DesiredStateValue returns the instance's current desired state.
func (i *Instance) DesiredStateValue() DesiredState {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.Meta.DesiredState
}

// SetDesiredState updates the instance's desired state (see handleSuspend/
// handleResume/handleKill).
func (i *Instance) SetDesiredState(s DesiredState) {
	i.mu.Lock()
	i.Meta.DesiredState = s
	i.mu.Unlock()
}

// SetPendingDestroy marks (or clears) the instance's PendingDestroy flag —
// set before any destructive step of /kill begins, so a crash mid-/kill is
// recoverable on the next restart (see Step 21).
func (i *Instance) SetPendingDestroy(v bool) {
	i.mu.Lock()
	i.Meta.PendingDestroy = v
	i.mu.Unlock()
}

// MetaSnapshot returns a consistent copy of the instance's full metadata —
// for read-heavy call sites (SaveMeta, /status, /list) that want every
// field as of one instant rather than several independently-locked reads.
func (i *Instance) MetaSnapshot() InstanceMeta {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.Meta
}
