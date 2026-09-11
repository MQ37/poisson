package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mq37/poisson/internal/auth"
	"github.com/mq37/poisson/internal/config"
	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/sandbox"
	"github.com/mq37/poisson/internal/store"
	"github.com/mq37/poisson/internal/subagent"
)

// maxConcurrentSubagents caps concurrently-RUNNING subagent child processes,
// process-wide — not just within one batch call. agent.maxConcurrentToolCalls
// and batch.go's own batchMaxConcurrent (same value, 8) each already cap
// their OWN fan-out, but neither knows about the other: nothing stops
// several of an agent round's 8 top-level tool_use slots from each
// independently being their own `batch` call, each spawning its own 8-wide
// subagent fan-out — up to 8×8=64 concurrent child processes system-wide
// (found scouting), well past the documented "8 max concurrent" ceiling.
const maxConcurrentSubagents = 8

// subagentSlots is acquired before every subagent.Spawn and released only
// once that child has been fully reaped (see Execute), so the combined
// in-flight total across every batch/round — no matter how deeply nested —
// really can't exceed maxConcurrentSubagents.
var subagentSlots = make(chan struct{}, maxConcurrentSubagents)

// removeDBFiles deletes a SQLite database and its WAL/SHM sidecars.
func removeDBFiles(path string) {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(path + suffix)
	}
}

// SubagentApproval handles bash approval requests forwarded from the child.
// risk is the child's assessed level (low/medium/high); empty means unknown.
// reason is an optional human-supplied explanation when denied.
type SubagentApproval func(command, description, workdir, agentName, risk string) (allowed bool, reason string)

// SubagentTool spawns a one-shot child Poisson process for isolated work. The
// child's conversation is ephemeral (a throwaway temp DB) and its internal
// steps never enter the parent's conversation — only the final result is
// returned to the calling model. The parent UI shows a compact widget derived
// from the tool_start / tool_result events, not the child's steps.
type SubagentTool struct {
	cwd        string
	providerFn func() string
	modelFn    func() string
	effortFn   func() string
	// cfgFn resolves the live config, used to validate a model/effort
	// override in Execute and to build the curated model list Description()
	// surfaces. nil (the default, until SetConfigFn is called) means overrides
	// are rejected outright — same fail-closed default this file already uses
	// for provider/model resolution.
	cfgFn           func() *config.Config
	skillsEnabledFn func() bool // nil, or unset result, means "enabled" (matches the main session's default)
	approvalFn      SubagentApproval

	// live tracks currently-running child processes so ExpediteAll can nudge
	// them to wrap up. Guarded by liveMu; touched from parallel tool goroutines
	// (register/unregister) and the TUI goroutine (ExpediteAll).
	liveMu sync.Mutex
	live   map[*subagent.ChildProcess]struct{}

	// progressFn reports a live turn-count + context-usage update for the
	// running widget, correlated via the tool_call ID attached to Execute's
	// context. status is normally "" (ordinary progress); when the child is
	// mid-network-retry it carries a short human-readable status ("connection
	// lost: ... — reconnecting…" / "reconnected — resuming") for the widget
	// to show in place of the turn/context line. tokensPerSec is the child's
	// own token-weighted average inference speed across every round it has
	// completed so far (0 if none reported yet).
	progressFn func(toolCallID string, turns, contextTokens, contextWindow int, tokensPerSec float64, status string)

	// usageFn records a finished (or partially finished) subagent's
	// accumulated token usage as a "subagent" api_calls row on the parent
	// session, so the spend counts toward the parent's /cost and status-bar
	// total instead of vanishing with the child's ephemeral, throwaway DB.
	// Returns the computed cost. nil means no recorder wired (e.g. tests
	// that don't care) — Execute treats that as "nothing to record".
	// childCost is the child's own cumulative cost, priced per call against
	// whichever model served it (see subagent.ChildEvent.Cost).
	usageFn func(providerID, model string, usage *provider.Usage, childCost float64) (float64, error)

	// classifierModelFn resolves the parent session's bash-risk classifier
	// model, propagated to the child so a /classifier-model pin covers the
	// whole px instance instead of stopping at the process boundary. nil (or
	// an empty result) leaves the child to resolve its own.
	classifierModelFn func() string

	// sandboxMgr, when non-nil, lets this call authorize specific sandboxIds
	// for the spawned child (see docs/sandbox-plan.md's subagent allow-list).
	// nil (the default) means sandboxing isn't available in this session at
	// all — a sandboxIds request then fails clearly instead of silently
	// being ignored.
	sandboxMgr *sandbox.Manager

	// authStore backs the IsConfigured check a cross-provider override runs
	// before ever asking a human or spawning a child (see
	// crossProviderApprovalFn) — nil-safe (auth.AuthStore is a plain map),
	// so an unset value just makes every provider read as unconfigured.
	authStore auth.AuthStore

	// crossProviderApprovalFn gates a model override that names a DIFFERENT
	// provider than the main session's own (see Execute's "provider/model"
	// parsing). nil means cross-provider overrides fail closed with a clear
	// error instead of silently denying or panicking — same discipline as
	// cfgFn being nil for a same-provider override.
	crossProviderApprovalFn ApprovalFn

	// bgCtx is the session-scoped context a spawned job actually runs on —
	// NOT the per-call ctx Execute receives, which dies the instant the
	// turn that called Execute ends (internal/tui/agent_io.go's startTurn
	// unconditionally cancels it in the turn goroutine's deferred cleanup).
	// A job's own child process would otherwise be killed within
	// microseconds of its own spawn-ack turn completing — see
	// docs/async-subagent-plan.md §A. nil (until SetBackgroundContext is
	// called) falls back to context.Background() in Execute: still correct,
	// just with no way to bulk-cancel outstanding jobs before process exit.
	bgCtx context.Context

	// jobs tracks every async job this tool has ever spawned, by ID —
	// process-lifetime, in-memory only (same "ephemeral, gone on restart"
	// framing already used for a child's own throwaway DB). Deliberately
	// NOT keyed to session/store state: /undo and /fork only ever mutate
	// message rows and have no path to this map, so a running job survives
	// either untouched (see docs/async-subagent-plan.md's inventory table).
	jobsMu sync.Mutex
	jobs   map[string]*subagentJob

	// jobDoneFn, if set, is called once a job reaches a terminal state
	// (done/error) — the TUI's signal to flip that job's widget from
	// "spawned" to actually finished, arbitrarily later than the tool_use
	// that spawned it (whose own tool_result was just the immediate spawn
	// ack — see docs/async-subagent-plan.md §D), and to notify the main
	// agent so it can react without being re-prompted (see phase 3). nil
	// means no one's listening (e.g. headless/`-p` mode, or tests that
	// don't care). sessionID is whatever sessionIDFn reported when the job
	// was spawned ("" if sessionIDFn was never wired) — the caller's cue to
	// skip reacting if the user has since switched to a different session.
	jobDoneFn func(jobID, sessionID string, res ToolResult)

	// sessionIDFn resolves the CURRENT session id at spawn time (a job
	// records it once, permanently, on subagentJob.sessionID) and again on
	// every getJob/listJobs lookup, so a job spawned under a session the
	// user has since `/new`'d or `/resume`'d away from doesn't leak into
	// the new one — neither via subagent_status/subagent_result nor via the
	// phase-3 auto-notify (see docs/async-subagent-plan.md phase 3's
	// cross-session-leakage finding). nil (unwired — e.g. most tests, or a
	// future non-TUI entry point) means "no session concept available":
	// every job is then visible to every caller, matching pre-phase-3
	// behavior exactly — fail OPEN, not closed, so nothing already shipped
	// silently starts hiding jobs just because this resolver isn't wired.
	sessionIDFn func() string
}

// SetSandboxManager wires the Manager that a sandboxIds request validates
// against. Optional — nil (the default) makes any non-empty sandboxIds
// input fail with a clear error instead of a nil pointer panic.
func (t *SubagentTool) SetSandboxManager(mgr *sandbox.Manager) {
	t.sandboxMgr = mgr
}

// SetAuth wires the auth store a cross-provider override's IsConfigured
// check reads. Optional — nil (the default) makes every provider read as
// unconfigured, which only matters once a cross-provider override is
// actually requested.
func (t *SubagentTool) SetAuth(a auth.AuthStore) {
	t.authStore = a
}

// SetCrossProviderApprovalFn wires the human-approval gate a model override
// naming a different provider than the main session's must pass before this
// tool spawns anything on it (see crossProviderApprovalFn's doc comment).
func (t *SubagentTool) SetCrossProviderApprovalFn(fn ApprovalFn) {
	t.crossProviderApprovalFn = fn
}

// NewSubagentTool creates a subagent tool.
func NewSubagentTool(cwd string, approvalFn SubagentApproval) *SubagentTool {
	return &SubagentTool{
		cwd:        cwd,
		approvalFn: approvalFn,
		live:       make(map[*subagent.ChildProcess]struct{}),
		jobs:       make(map[string]*subagentJob),
	}
}

// SetBackgroundContext supplies the session-scoped context every spawned
// job actually runs on (see bgCtx's doc comment). Optional — the safe
// default (context.Background()) is used when unset.
func (t *SubagentTool) SetBackgroundContext(ctx context.Context) {
	t.bgCtx = ctx
}

// SetJobDoneFn supplies the callback fired once an async job actually
// finishes (see jobDoneFn's doc comment).
func (t *SubagentTool) SetJobDoneFn(fn func(jobID, sessionID string, res ToolResult)) {
	t.jobDoneFn = fn
}

// SetSessionIDFn supplies the live current-session-id resolver (see
// sessionIDFn's doc comment).
func (t *SubagentTool) SetSessionIDFn(fn func() string) {
	t.sessionIDFn = fn
}

// currentSessionID reports the live session id, or "" if sessionIDFn isn't
// wired.
func (t *SubagentTool) currentSessionID() string {
	if t.sessionIDFn == nil {
		return ""
	}
	return t.sessionIDFn()
}

// visibleToCurrentSession reports whether job should be visible/actionable
// from the CURRENT session — true whenever session tracking isn't wired at
// all (fail open, see sessionIDFn's doc comment), whenever the job itself
// was spawned before tracking existed (job.sessionID == ""), or whenever
// the job's recorded session still matches the live one.
func (t *SubagentTool) visibleToCurrentSession(job *subagentJob) bool {
	if t.sessionIDFn == nil || job.sessionID == "" {
		return true
	}
	return job.sessionID == t.currentSessionID()
}

func (t *SubagentTool) trackLive(c *subagent.ChildProcess) {
	t.liveMu.Lock()
	t.live[c] = struct{}{}
	t.liveMu.Unlock()
}

func (t *SubagentTool) untrackLive(c *subagent.ChildProcess) {
	t.liveMu.Lock()
	delete(t.live, c)
	t.liveMu.Unlock()
}

// ExpediteAll forwards a "finish now" nudge to every live subagent child,
// returning how many were signalled. Safe to call from any goroutine.
func (t *SubagentTool) ExpediteAll() int {
	t.liveMu.Lock()
	defer t.liveMu.Unlock()
	n := 0
	for c := range t.live {
		if c.SendExpedite() == nil {
			n++
		}
	}
	return n
}

// KillAll forcefully terminates every currently-live subagent child process
// — process group and descendant tree, see ChildProcess.Kill — and returns
// how many were signalled. Unlike ExpediteAll (a cooperative "wrap up"
// nudge the child can take its time acting on), this is unconditional and
// immediate: used when px itself is shutting down (Ctrl+C, Ctrl+D, /quit,
// SIGINT/SIGTERM/SIGHUP — see TUI.prepareShutdownLocked), where a still-
// running child (and anything it spawned, e.g. a bash sleep) must not be
// left orphaned just because nothing waited for it. Safe to call from any
// goroutine; each killed child's own runJob goroutine notices (its blocked
// ReadEvent returns an error once the pipe closes), finishes normally, and
// untracks itself from live — no manual cleanup needed here.
func (t *SubagentTool) KillAll() int {
	t.liveMu.Lock()
	defer t.liveMu.Unlock()
	n := 0
	for c := range t.live {
		if c.Kill() == nil {
			n++
		}
	}
	return n
}

// SetRuntime supplies live provider/model/effort resolvers (called at spawn time).
func (t *SubagentTool) SetRuntime(providerFn, modelFn, effortFn func() string) {
	t.providerFn = providerFn
	t.modelFn = modelFn
	t.effortFn = effortFn
}

// SetConfigFn supplies a live config resolver (see cfgFn's doc comment).
func (t *SubagentTool) SetConfigFn(cfgFn func() *config.Config) {
	t.cfgFn = cfgFn
}

// SetSkillsEnabledFn supplies a live resolver for whether the main session
// has skills enabled, so a spawned subagent mirrors that (SetSkills(false)
// / --no-skills means the whole tree, not just the parent, goes without).
func (t *SubagentTool) SetSkillsEnabledFn(fn func() bool) {
	t.skillsEnabledFn = fn
}

// SetProgressFn supplies the live turn-count + context-usage progress
// callback (called from Execute's goroutine as the child reports each new turn).
func (t *SubagentTool) SetProgressFn(fn func(toolCallID string, turns, contextTokens, contextWindow int, tokensPerSec float64, status string)) {
	t.progressFn = fn
}

// SetUsageFn supplies the callback that rolls a finished subagent's token
// usage into the parent session's cost (see usageFn's doc comment).
func (t *SubagentTool) SetUsageFn(fn func(providerID, model string, usage *provider.Usage, childCost float64) (float64, error)) {
	t.usageFn = fn
}

// SetClassifierModelFn supplies the resolver for the parent's bash-risk
// classifier model, propagated to every child this tool spawns (see
// classifierModelFn's doc comment).
func (t *SubagentTool) SetClassifierModelFn(fn func() string) {
	t.classifierModelFn = fn
}

func (t *SubagentTool) Name() string { return "subagent" }

// subagentBaseDescription is the tool description's static part — the
// model/effort override section is appended dynamically by Description(),
// since the available models depend on which provider is live.
const subagentBaseDescription = "Spawn a one-shot child Poisson agent to complete a specific task, in the background. Returns immediately with a job ID — it does NOT wait for the child to finish. The child has every tool you do (read, write, edit, bash, web_search, web_ask, recall) except the ability to spawn further subagents. Use when you need focused work isolated from the main session. It cannot ask questions — give it a complete, self-contained task. Use subagent_status to check a job's progress (or list every job) and subagent_result to retrieve the final output once it's done — each job's result can only be retrieved once. Optional sandboxIds shares specific sandboxes (from create_sandbox) with the child — it can only use ones named here, it cannot create its own."

// subagentEffortGuide is shared across every model — the five levels mean
// roughly the same thing regardless of which model they're applied to (per
// Anthropic's own effort docs); each model's own listing below states which
// of them it actually supports.
const subagentEffortGuide = "Effort levels (low -> max) trade capability for cost: low = cheap/scoped, medium = balanced, high = default, xhigh = hardest coding/agentic tasks, max = frontier-only and unconstrained. Reach for xhigh/max sparingly — they cost the most, both in dollars and in Claude subscription usage quota."

// subagentModelHelpHeader explains the "provider/model" qualified override
// syntax and its approval gate — see Execute's model-parsing comment for the
// one documented edge case (a model ID whose own first path segment
// happens to collide with a configured custom provider's name).
const subagentModelHelpHeader = "Optional model/effort override for this subagent only (default: inherit the main session's model/effort). Model may be a bare model ID — same provider as the main session, runs immediately, no approval — or a \"provider/model\" qualified ID to run the subagent on a DIFFERENT provider, which needs human approval before the subagent starts, same as a risky bash command — unless both providers are listed under [subagent] trusted_providers in config.toml, in which case it runs immediately (the tags below say which is which). (If a bare model ID's own first path segment happens to name a configured custom provider, qualify it explicitly with its real provider to avoid misparsing, e.g. \"llamacpp/unsloth/Laguna-S-2.1-GGUF\".) Providers and models:"

// Description lists every provider Poisson knows about — not just the one
// currently active — since a subagent may now be spawned on any of them (a
// bare model ID stays on the main session's provider and auto-runs; a
// "provider/model" qualified ID targets any other one but always needs
// human approval first). A provider is skipped only when it has nothing
// nameable to offer: no curated models AND not configured AND isn't the
// main provider. Falls back to the static base text when the runtime
// resolvers (SetRuntime/SetConfigFn) haven't been wired yet, or no provider
// has anything to list — never a broken or half-rendered description.
func (t *SubagentTool) Description() string {
	if t.cfgFn == nil || t.providerFn == nil {
		return subagentBaseDescription
	}
	mainProv := t.providerFn()
	if mainProv == "" {
		return subagentBaseDescription
	}
	cfg := t.cfgFn()
	var b strings.Builder
	for _, meta := range config.AllProviderMeta(cfg) {
		configured := provider.IsConfigured(meta.ID, t.authStore, cfg)
		list := provider.FormatModelsForPrompt(cfg, meta.ID)
		if list == "" && !configured && meta.ID != mainProv {
			continue
		}
		tag := "different provider — requires human approval before spawning, same as a risky bash command"
		switch {
		case meta.ID == mainProv:
			tag = "current provider — auto-runs, no approval needed"
		case !configured:
			// Wins over the trusted case below: Execute() rejects an
			// unconfigured target regardless of trust, so this must never
			// be advertised as auto-run.
			tag = "different provider, NOT CONFIGURED — requires approval AND will fail to spawn until credentials are set"
		case cfg.ProvidersMutuallyTrusted(mainProv, meta.ID):
			tag = "different provider, mutually trusted with the current one — auto-runs, no approval needed"
		}
		fmt.Fprintf(&b, "\n%s (%s):\n", meta.ID, tag)
		if list == "" {
			b.WriteString("  (no curated models listed — pass an exact model ID you already know is valid)\n")
		} else {
			b.WriteString(list)
			b.WriteString("\n")
		}
	}
	if b.Len() == 0 {
		return subagentBaseDescription
	}
	return subagentBaseDescription + "\n\n" + subagentModelHelpHeader + b.String() + "\n" + subagentEffortGuide
}

func (t *SubagentTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"task": {"type": "string", "description": "Complete, self-contained task for the subagent. Include context, file paths, and expected output format."},
			"name": {"type": "string", "description": "Display name for the subagent. If omitted, a name is chosen automatically."},
			"sandboxIds": {"type": "array", "items": {"type": "string"}, "description": "Sandboxes (from create_sandbox) to let this child use — each id must be one this session actually created. The child cannot create its own sandboxes."},
			"model": {"type": "string", "description": "Override model for this subagent only. A bare model ID stays on the main session's provider (auto-runs, no approval). A \"provider/model\" qualified ID targets a different provider — may require human approval before the subagent starts, see this tool's description. Default: inherit the main session's model."},
			"effort": {"type": "string", "enum": ["low", "medium", "high", "xhigh", "max"], "description": "Override effort for this subagent only. Default: inherit the main session's effort."}
		},
		"required": ["task"]
	}`)
}

func (t *SubagentTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	var params struct {
		Task       string   `json:"task"`
		Name       string   `json:"name"`
		SandboxIDs []string `json:"sandboxIds"`
		Model      string   `json:"model"`
		Effort     string   `json:"effort"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return ToolResult{Error: "invalid input: " + err.Error()}, nil
	}
	if params.Task == "" {
		return ToolResult{Error: "task is required"}, nil
	}

	agentName := params.Name
	if agentName == "" {
		agentName = "subagent"
	}

	// Resolve every requested sandboxId against this session's own Manager
	// before spawning anything — same untrusted-input discipline as bash's
	// own sandboxId handling: a hallucinated or foreign id must fail loudly,
	// not be silently dropped or discovered as a confusing error inside the
	// child later.
	var authorizedSandboxes []subagent.SandboxAuth
	if len(params.SandboxIDs) > 0 {
		if t.sandboxMgr == nil {
			return ToolResult{Error: "sandboxIds given but sandboxing is not available in this session"}, nil
		}
		for _, id := range params.SandboxIDs {
			sb, ok := t.sandboxMgr.Get(id)
			if !ok {
				return ToolResult{Error: fmt.Sprintf("sandbox %q not found — it may belong to a different session, have been destroyed, or never existed", id)}, nil
			}
			authorizedSandboxes = append(authorizedSandboxes, subagent.SandboxAuth{ID: sb.ID, HostPath: sb.HostPath})
		}
	}

	// A subagent defaults to the exact same provider + model as the main
	// session — never a silent fallback to some other model, which would
	// change cost/behavior/quality without the user ever choosing it. If the
	// resolvers aren't wired or the main session can't report a provider/model
	// right now, that's a real configuration problem: fail loudly instead of
	// guessing. An explicit, human-approved cross-provider override is the
	// one deliberate exception — see the params.Model handling below.
	if t.providerFn == nil || t.modelFn == nil {
		return ToolResult{Error: "subagent runtime not configured: provider/model resolver missing"}, nil
	}
	prov := t.providerFn()
	if prov == "" {
		return ToolResult{Error: "subagent cannot start: main session reported no provider"}, nil
	}
	model := t.modelFn()
	if model == "" {
		return ToolResult{Error: "subagent cannot start: main session reported no model"}, nil
	}
	effort := ""
	if t.effortFn != nil {
		effort = t.effortFn()
	}

	// Optional per-subagent model/effort override. params.Model may be a
	// bare model ID (same provider as the main session, prov above) or a
	// "provider/model" qualified ID naming a DIFFERENT provider — split on
	// the first "/" only (provider IDs never contain one; a model ID that
	// does, e.g. llamacpp's "unsloth/Laguna-S-2.1-GGUF", is safe because its
	// own first segment isn't a real provider name — see
	// subagentModelHelpHeader's documented exception for the rare case a
	// custom provider happens to collide with one). Rejects unknown
	// models/efforts, an unconfigured target provider, or a denied approval
	// loudly instead of silently falling back to the inherited default —
	// same fail-closed discipline as every other input this tool validates.
	trustNotice := ""
	if params.Model != "" || params.Effort != "" {
		if t.cfgFn == nil {
			return ToolResult{Error: "model/effort override requested but not supported in this session"}, nil
		}
		cfg := t.cfgFn()
		if params.Model != "" {
			reqProv, reqModel := prov, params.Model
			if p, m, hasSlash := strings.Cut(params.Model, "/"); hasSlash && m != "" {
				if _, ok := config.ResolveProviderMeta(p, cfg); ok {
					reqProv, reqModel = p, m
				}
			}
			crossProvider := reqProv != prov
			if crossProvider {
				// Check the more fundamental failures — no approval gate
				// wired, target provider unconfigured — before ever
				// validating the model name or asking a human about a
				// spawn that's guaranteed to fail anyway.
				if t.crossProviderApprovalFn == nil {
					return ToolResult{Error: "cross-provider subagent spawn requested but approval isn't wired in this session"}, nil
				}
				if !provider.IsConfigured(reqProv, t.authStore, cfg) {
					return ToolResult{Error: fmt.Sprintf("provider %q is not configured — no credentials found", reqProv)}, nil
				}
			}
			if _, ok := provider.MergedModelSettings(cfg, reqProv, reqModel); !ok {
				return ToolResult{Error: fmt.Sprintf("unknown model %q for provider %q — see this tool's description for available models", reqModel, reqProv)}, nil
			}
			if crossProvider && cfg.ProvidersMutuallyTrusted(prov, reqProv) {
				// Both sides listed under [subagent] trusted_providers: skip
				// the human gate, but say so — a silent skip is how a config
				// knob turns into a surprise. The nil-approvalFn and
				// IsConfigured checks above still ran unconditionally.
				trustNotice = fmt.Sprintf("cross-provider approval skipped — %s/%s trusted by config.", prov, reqProv)
			} else if crossProvider {
				command := fmt.Sprintf("subagent -> %s/%s", reqProv, reqModel)
				reason := fmt.Sprintf("cross-provider subagent spawn (main session runs %s/%s)", prov, model)
				approved, denyReason := t.crossProviderApprovalFn(ctx, command, reason, t.cwd)
				if !approved {
					msg := "cross-provider subagent spawn denied"
					if denyReason != "" {
						msg += ": " + denyReason
					}
					return ToolResult{Error: msg}, nil
				}
			}
			prov, model = reqProv, reqModel
		}
		if params.Effort != "" {
			settings, _ := provider.MergedModelSettings(cfg, prov, model)
			if !effortLevelAllowed(settings, params.Effort) {
				return ToolResult{Error: fmt.Sprintf("effort %q not supported by model %q", params.Effort, model)}, nil
			}
			effort = params.Effort
		}
	}

	childSessionID := store.NewSubagentID()
	classifierModel := ""
	if t.classifierModelFn != nil {
		classifierModel = t.classifierModelFn()
	}

	// The child conversation is ephemeral: it lives in a throwaway DB under the
	// OS temp dir and is deleted when the subagent finishes, so nothing is
	// persisted to the parent's DB (same policy as /btw).
	dbPath := filepath.Join(os.TempDir(), "poisson-"+childSessionID+".db")

	spawnInput := subagent.SpawnInput{
		Task:                params.Task,
		Cwd:                 t.cwd,
		SessionID:           childSessionID,
		Name:                agentName,
		Provider:            prov,
		Model:               model,
		Effort:              effort,
		ClassifierModel:     classifierModel,
		NoSkills:            t.skillsEnabledFn != nil && !t.skillsEnabledFn(),
		DBPath:              dbPath,
		AuthorizedSandboxes: authorizedSandboxes,
	}

	job := &subagentJob{
		id: childSessionID, name: agentName, task: params.Task,
		provider: prov, model: model, effort: effort,
		sessionID: t.currentSessionID(),
		startedAt: time.Now(), status: "queued",
	}
	t.jobsMu.Lock()
	t.jobs[job.id] = job
	t.jobsMu.Unlock()

	// toolCallID is captured from the REQUEST ctx (this call's own tool_use)
	// before the goroutine below ever starts — runJob runs on bgCtx instead,
	// which carries no such value (see bgCtx's doc comment).
	toolCallID, hasToolCallID := ToolCallIDFromContext(ctx)
	bgCtx := t.bgCtx
	if bgCtx == nil {
		bgCtx = context.Background()
	}
	go t.runJob(bgCtx, job, spawnInput, dbPath, toolCallID, hasToolCallID)

	ack := fmt.Sprintf(
		"Subagent %q spawned as job %s. It runs in the background — use subagent_status to check progress, subagent_result to retrieve the final output once done.",
		agentName, job.id,
	)
	if trustNotice != "" {
		ack = trustNotice + " " + ack
	}
	return ToolResult{Content: ack}, nil
}

// subagentJob tracks one asynchronously-spawned subagent job, independent
// of the tool_use that spawned it — see docs/async-subagent-plan.md.
// id/name/task/provider/model/effort/startedAt are set once at creation and
// never change; everything else is mutable, guarded by mu.
type subagentJob struct {
	id, name, task, provider, model, effort string
	// sessionID is the session that spawned this job, captured once at
	// creation via sessionIDFn — "" if that resolver wasn't wired. See
	// SubagentTool.visibleToCurrentSession.
	sessionID string
	startedAt time.Time

	mu            sync.Mutex
	status        string // "queued" | "running" | "done" | "error"
	turns         int
	toolCount     int
	contextTokens int
	contextWindow int
	tokensPerSec  float64
	doneAt        time.Time
	result        ToolResult // set once, when status becomes "done" or "error"
	retrieved     bool
}

// subagentJobView is a point-in-time, lock-free copy of a subagentJob for
// subagent_status/subagent_result — copying a struct that embeds
// sync.Mutex directly would copy the lock itself, which go vet flags.
type subagentJobView struct {
	id, name, task, provider, model, effort        string
	startedAt, doneAt                              time.Time
	status                                         string
	turns, toolCount, contextTokens, contextWindow int
	tokensPerSec                                   float64
	result                                          ToolResult
	retrieved                                       bool
}

func (j *subagentJob) view() subagentJobView {
	j.mu.Lock()
	defer j.mu.Unlock()
	return subagentJobView{
		id: j.id, name: j.name, task: j.task, provider: j.provider, model: j.model, effort: j.effort,
		startedAt: j.startedAt, doneAt: j.doneAt, status: j.status,
		turns: j.turns, toolCount: j.toolCount, contextTokens: j.contextTokens, contextWindow: j.contextWindow,
		tokensPerSec: j.tokensPerSec, result: j.result, retrieved: j.retrieved,
	}
}

// getJob looks up a job by ID, scoped to the current session (see
// visibleToCurrentSession) — a job spawned under a session the caller has
// since switched away from reads as not-found, same as if it never
// existed, rather than leaking across the switch.
func (t *SubagentTool) getJob(id string) (*subagentJob, bool) {
	t.jobsMu.Lock()
	j, ok := t.jobs[id]
	t.jobsMu.Unlock()
	if !ok || !t.visibleToCurrentSession(j) {
		return nil, false
	}
	return j, true
}

// listJobs returns every job this tool has spawned that's visible to the
// current session (see visibleToCurrentSession), oldest first.
func (t *SubagentTool) listJobs() []subagentJobView {
	t.jobsMu.Lock()
	jobs := make([]*subagentJob, 0, len(t.jobs))
	for _, j := range t.jobs {
		if t.visibleToCurrentSession(j) {
			jobs = append(jobs, j)
		}
	}
	t.jobsMu.Unlock()
	sort.Slice(jobs, func(i, k int) bool { return jobs[i].startedAt.Before(jobs[k].startedAt) })
	views := make([]subagentJobView, len(jobs))
	for i, j := range jobs {
		views[i] = j.view()
	}
	return views
}

// retrieveJob returns a job's final result exactly once — a second call
// after the first successful retrieval errors instead of repeating it (see
// docs/async-subagent-plan.md §C's one-shot decision), forcing the model to
// use/remember the answer instead of re-fetching it as a free memory jog.
// subagent_status stays freely repeatable for progress-checking.
func (t *SubagentTool) retrieveJob(job *subagentJob) ToolResult {
	job.mu.Lock()
	defer job.mu.Unlock()
	switch job.status {
	case "queued", "running":
		return ToolResult{Error: fmt.Sprintf("job %s is still %s — check subagent_status", job.id, job.status)}
	case "done", "error":
		if job.retrieved {
			return ToolResult{Error: fmt.Sprintf("job %s result already retrieved", job.id)}
		}
		job.retrieved = true
		return job.result
	default:
		return ToolResult{Error: fmt.Sprintf("job %s has unknown status %q", job.id, job.status)}
	}
}

// runJob runs one subagent job's entire lifecycle in the background, on ctx
// (the session-scoped background context — see bgCtx's doc comment), never
// the per-call ctx Execute received. This is Execute's old synchronous body
// (unchanged behavior), writing outcomes into job instead of returning them
// directly, so subagent_status/subagent_result can observe them at any
// point — including turns long after the spawning call already returned.
func (t *SubagentTool) runJob(ctx context.Context, job *subagentJob, spawnInput subagent.SpawnInput, dbPath, toolCallID string, hasToolCallID bool) {
	defer removeDBFiles(dbPath)

	// Block for a global concurrency slot before spawning a real OS process
	// — see maxConcurrentSubagents' doc comment. Released only after the
	// child is fully reaped below (defer registered before child.Reap's, so
	// it runs after — LIFO), not merely after Spawn returns, so the slot
	// reflects an actually-running process the whole time it's alive.
	select {
	case subagentSlots <- struct{}{}:
	case <-ctx.Done():
		res := ToolResult{Error: "subagent cancelled while waiting for a concurrency slot"}
		job.mu.Lock()
		job.status, job.doneAt = "error", time.Now()
		job.result = res
		job.mu.Unlock()
		if t.jobDoneFn != nil {
			t.jobDoneFn(job.id, job.sessionID, res)
		}
		return
	}
	defer func() { <-subagentSlots }()

	child, err := subagent.Spawn(spawnInput)
	if err != nil {
		res := ToolResult{Error: "failed to spawn subagent: " + err.Error()}
		job.mu.Lock()
		job.status, job.doneAt = "error", time.Now()
		job.result = res
		job.mu.Unlock()
		if t.jobDoneFn != nil {
			t.jobDoneFn(job.id, job.sessionID, res)
		}
		return
	}
	// "running" means the child process actually exists now — set only
	// after Spawn succeeds, not merely "we're about to try", so a caller
	// polling for status != "queued" (e.g. subagent_status, or a test
	// synchronizing before tearing down a SetLookupExecutableForTest
	// fixture) has a real guarantee the process is live.
	job.mu.Lock()
	job.status = "running"
	job.mu.Unlock()
	defer child.Reap()
	t.trackLive(child)
	defer t.untrackLive(child)

	prov, model, effort := job.provider, job.model, job.effort

	var output strings.Builder
	var toolCount, turns, contextTokens, contextWindow int
	// speedTokenSum/speedRateSum accumulate the child's rounds into a
	// token-weighted average output tokens/sec — Σ(rate × tokens) / Σ(tokens),
	// exactly what the main conversation's header shows for its own rounds
	// (see tui.scrollback.avgTokensPerSec). A raw per-round figure swings
	// wildly because its denominator includes connection setup, prefill/TTFT
	// and any provider backoff, so a short tool-call-only round reads far
	// lower than a long answer at the same real decode speed; weighting by
	// output tokens lets the big rounds dominate and keeps the widget stable.
	var speedTokenSum, speedRateSum float64
	var tokensPerSec float64
	var success bool
	var childErr string
	// reportProgress both mirrors the live turn/context/speed figures onto
	// job (so subagent_status can read them at any time) and, when this call
	// still has a live tool_call widget (hasToolCallID, passed in from
	// Execute's own request ctx — bgCtx carries no such value), forwards the
	// same update to the TUI exactly as the old synchronous path did.
	reportProgress := func(status string) {
		job.mu.Lock()
		job.turns, job.toolCount = turns, toolCount
		job.contextTokens, job.contextWindow = contextTokens, contextWindow
		job.tokensPerSec = tokensPerSec
		job.mu.Unlock()
		if hasToolCallID && t.progressFn != nil {
			t.progressFn(toolCallID, turns, contextTokens, contextWindow, tokensPerSec, status)
		}
	}

	// lastUsage holds the child's most recently reported cumulative token
	// usage (relayed on every "tool" tick and on "done", see ChildEvent.Usage)
	// and recordedCost/recorded track whether it's already been rolled into
	// the parent session. Recording explicitly happens at the "done" event so
	// the cost can be folded into the returned summary text; the deferred
	// call below is a guarded fallback that fires the same recording exactly
	// once for every OTHER return path in this function (ctx cancelled,
	// approval-send failure, etc.) — a subagent killed mid-run by a cancelled
	// parent turn still gets partial credit for whatever it had already
	// spent as of its last progress tick.
	// lastChildCost rides along with it: the child prices every call against
	// the model that served it, so its own figure is the only correct one for
	// a run that mixed models (e.g. a bash-risk classifier pinned to a
	// different model than the child's main one).
	var lastUsage provider.Usage
	var lastChildCost float64
	var usageSeen, recorded bool
	var recordedCost float64
	bankUsage := func(ev *subagent.ChildEvent) {
		if ev.Usage == nil {
			return
		}
		lastUsage, lastChildCost, usageSeen = *ev.Usage, ev.Cost, true
	}
	recordUsage := func() {
		if recorded || !usageSeen || t.usageFn == nil {
			return
		}
		if lastUsage.InputTokens == 0 && lastUsage.OutputTokens == 0 &&
			lastUsage.CacheReadTokens == 0 && lastUsage.CacheWriteTokens == 0 {
			return // nothing billed yet (e.g. cancelled before the child's first turn completed)
		}
		recorded = true
		cost, err := t.usageFn(prov, model, &lastUsage, lastChildCost)
		if err != nil {
			log.Printf("warning: record subagent usage: %v", err)
			return
		}
		recordedCost = cost
	}
	defer recordUsage()

	// finishJob records final usage, writes the last-known progress figures,
	// and stores res as the job's one-shot result (see retrieveJob) — the
	// single place every exit path below converges on, instead of each
	// duplicating that bookkeeping.
	finishJob := func(res ToolResult) {
		recordUsage()
		job.mu.Lock()
		job.doneAt = time.Now()
		job.turns, job.toolCount = turns, toolCount
		job.contextTokens, job.contextWindow = contextTokens, contextWindow
		job.tokensPerSec = tokensPerSec
		if res.Error != "" {
			job.status = "error"
		} else {
			job.status = "done"
		}
		job.result = res
		job.mu.Unlock()
		if t.jobDoneFn != nil {
			t.jobDoneFn(job.id, job.sessionID, res)
		}
	}

	for {
		if err := ctx.Err(); err != nil {
			finishJob(ToolResult{Content: output.String(), Error: "subagent cancelled"})
			return
		}

		type readResult struct {
			ev  *subagent.ChildEvent
			err error
		}
		readCh := make(chan readResult, 1)
		go func() {
			ev, readErr := child.ReadEvent()
			readCh <- readResult{ev: ev, err: readErr}
		}()

		select {
		case <-ctx.Done():
			finishJob(ToolResult{Content: output.String(), Error: "subagent cancelled"})
			return
		case res := <-readCh:
			if res.err != nil {
				if ctx.Err() != nil {
					finishJob(ToolResult{Content: output.String(), Error: "subagent cancelled"})
					return
				}
				if res.err.Error() != "" && childErr == "" {
					childErr = res.err.Error()
				}
				goto done
			}
			ev := res.ev
			if ev == nil {
				continue
			}

			// The child's internal steps (text, tool calls, tool results) are
			// intentionally NOT forwarded to the parent UI: only the final result
			// is returned to the calling model, and the parent shows a compact
			// widget. We still accumulate text + count tools for the summary.
			switch ev.Type {
			case "text":
				output.WriteString(ev.Text)

			case "tool":
				toolCount++
				if ev.Turns > 0 {
					turns = ev.Turns
					contextTokens, contextWindow = ev.ContextTokens, ev.ContextWindow
					// A real progress update means the child is actively working
					// again, so this also clears any "reconnecting" status the
					// widget was showing.
					reportProgress("")
				}
				bankUsage(ev)

			case "retrying":
				// Relayed from the child's own network-retry notice (see
				// agent.OutputRetrying) so the widget can show "reconnecting"
				// instead of freezing on stale turn/context numbers with no
				// explanation while the child's connection recovers.
				reportProgress(ev.Text)

			case "speed":
				// Relayed from the child's own agent.OutputInferenceSpeed —
				// only sent by the child when it has an actual reading (see
				// forwardChildEvents), so this is always a real update.
				weight := float64(ev.OutputTokens)
				if weight <= 0 {
					weight = 1 // no weight reported: count the round once
				}
				speedTokenSum += weight
				speedRateSum += ev.TokensPerSec * weight
				tokensPerSec = speedRateSum / speedTokenSum
				reportProgress("")

			case "tool_result":

			case "approval_request":
				// Bank the child's spend so far before blocking on a human:
				// this event carries the usage of the risk-classification call
				// that produced the verdict being shown, and the wait below can
				// end in cancellation with no further event ever arriving.
				bankUsage(ev)
				if ctx.Err() != nil {
					goto done
				}
				approved, reason := false, ""
				if t.approvalFn != nil {
					approved, reason = t.approvalFn(ev.Command, ev.Description, ev.Cwd, ev.Agent, ev.Risk)
				}
				if err := child.SendApprovalSafe(approved, reason); err != nil {
					childErr = "approval response failed: " + err.Error()
					goto done
				}

			case "done":
				success = ev.Success
				if ev.Turns > 0 {
					turns = ev.Turns
					contextTokens, contextWindow = ev.ContextTokens, ev.ContextWindow
					reportProgress("") // final count, before the card flips to done
				}
				bankUsage(ev)
				if ev.Error != "" {
					childErr = ev.Error
				}
				goto done

			case "error":
				childErr = ev.Error
				goto done
			}
		}
	}

done:
	result := output.String()
	result += fmt.Sprintf("\n\n---\nSubagent finished. %d tool calls, %d turns.", toolCount, turns)
	// recordUsage() here (rather than leaving it entirely to finishJob) so
	// the cost, once known, can be folded into the summary text below —
	// finishJob's own call is then a no-op (recordUsage is idempotent,
	// guarded by `recorded`).
	recordUsage()
	if recorded {
		result += fmt.Sprintf(" Cost: $%.4f.", recordedCost)
	}
	// "Ran on <provider>/<model> (<effort> effort)." — the durable record of
	// what this call actually ran on, parsed back out by the TUI (see
	// subagentRanOnFromResult) to label the widget correctly even after the
	// main session later switches models, or on a resumed session where
	// re-deriving it from the CURRENT agent would silently show the wrong,
	// ever-changing model for old history. Same "smuggle it in the result
	// text" pattern this file already uses for Cost above, for the same
	// reason: the child's ephemeral DB is gone by the time anyone would want
	// to ask it directly.
	result += fmt.Sprintf(" Ran on %s/%s", prov, model)
	if effort != "" {
		result += " (" + effort + " effort)"
	}
	result += "."
	if childErr != "" {
		result += "\nError: " + childErr
		finishJob(ToolResult{Content: result, Error: childErr})
		return
	}
	if !success {
		result += " (subagent reported failure)"
	}
	finishJob(ToolResult{Content: result})
}

// effortLevelAllowed reports whether level is one of settings' EffortLevels.
// A model with none listed (SupportsEffort false, or effort simply unlisted)
// rejects every explicit override — same fail-closed default as everything
// else in this override path.
func effortLevelAllowed(settings provider.ModelSettings, level string) bool {
	for _, l := range settings.EffortLevels {
		if l == level {
			return true
		}
	}
	return false
}
