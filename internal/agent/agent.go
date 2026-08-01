// Package agent implements the Poisson agent loop (SPEC §17): ingest → build
// context → stream → dispatch tools → compact if needed → commit.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mq37/poisson/internal/config"
	"github.com/mq37/poisson/internal/pricing"
	"github.com/mq37/poisson/internal/project"
	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/skills"
	"github.com/mq37/poisson/internal/store"
	"github.com/mq37/poisson/internal/tools"
)

// Output event type constants used by the TUI package.
const (
	OutputText       = "text"
	OutputThinking   = "thinking"
	OutputToolStart  = "tool_start"
	OutputToolResult = "tool_result"
	OutputStatus     = "status"
	OutputApproval   = "approval"
	OutputError      = "error"
	OutputCompacting = "compacting"
	OutputCompacted  = "compacted"
	// OutputRetrying carries a network-resilience notice: the provider hit a
	// connection failure or transient server error (5xx/429) and is retrying
	// with backoff (see provider.DoWithRetry). Sent at most twice per outage
	// — once when a fresh outage starts, once when it recovers — never once
	// per attempt, so a long outage doesn't spam the conversation.
	OutputRetrying = "retrying"
	OutputDone     = "done"
	// OutputSubagentProgress carries a live turn-count + context-usage update
	// for a running subagent widget. ToolCallID correlates to the
	// OutputToolStart that created it; SubagentTurns is the child's turn count
	// so far, and ContextTokens/ContextWindow (reused from the status fields)
	// are the child's own context usage.
	OutputSubagentProgress = "subagent_progress"
	// OutputInferenceSpeed reports the average output tokens/sec for one
	// completed streaming round: the provider's exact OutputTokens (reused
	// from the status field) divided by wall-clock time from request-sent to
	// EventDone. Always sent exactly once per round (see
	// sendInferenceSpeedEvent — TokensPerSec is left at zero, never shown,
	// when there's nothing measurable to report), after every block that
	// round produced (thinking, answer text, tool calls) already exists in
	// the TUI — which applies TokensPerSec to all of them, since no provider
	// breaks usage down more finely than "total output tokens for this
	// response". Sending every round unconditionally (not skipping the
	// no-reading case) matters: it's also how the TUI clears its "blocks from
	// the round in flight" bookkeeping, so skipping it would leak this
	// round's blocks into the next round's figure.
	OutputInferenceSpeed = "inference_speed"
)

// OutputEvent is a serialized terminal rendering event. The TUI goroutine
// drains these from the agent's outputChan and renders them.
type OutputEvent struct {
	Type              string          // text | tool_start | tool_result | status | approval | error | compacting
	Text              string          // text | error | compacting
	ToolName          string          // tool_start | tool_result
	ToolCallID        string          // tool_start | tool_result (provider call id)
	ToolInput         json.RawMessage // tool_start
	ToolResultContent string          // tool_result
	ToolError         string          // tool_result
	// HumanApproval reports whether a live human was actually asked to
	// approve this call, and what they decided — "" (never asked, the
	// common case: guard/LLM auto-approved), "approved", or "denied". See
	// tools.ApprovalRecord.
	HumanApproval        string  // tool_result
	ContextPct           float64 // status
	ContextTokens        int     // status, subagent_progress
	ContextWindow        int     // status, subagent_progress
	Cost                 float64 // status
	Model                string  // status
	OutputTokens         int     // status, inference_speed (this round's exact output tokens)
	CacheReadTokens      int     // status
	CacheWriteTokens     int     // status
	CallCount            int     // status
	ToolCalls            int     // status
	ToolErrors           int     // status
	Effort               string  // status
	SubagentTurns        int     // subagent_progress
	TokensPerSec         float64 // inference_speed
	SubagentTokensPerSec float64 // subagent_progress (child's own inference speed)

	CompactionTokensBefore int  // compacted
	CompactionTokensAfter  int  // compacted
	ThinkingRedacted       bool // thinking (opaque redacted block)
}

// Agent runs the turn loop for a single session.
type Agent struct {
	store      *store.Store
	provider   provider.Provider
	tools      *tools.Registry
	config     *config.Config
	sessionID  string
	outputChan chan OutputEvent
	approvalFn func(ctx context.Context, command, description, workdir string) (bool, string)
	model      string
	effort     string

	// classifierModels overrides which model rates bash-command risk (see
	// risk.go), keyed by provider ID so switching provider back and forth
	// keeps each choice. Set from the TUI's /classifier-model; session-scoped
	// (never persisted). Empty/missing entry falls back to
	// config.Classifier.Model and then to the session's own model.
	//
	// classifierMu guards it because — unlike /effort and /model, which the
	// TUI refuses mid-turn — /classifier-model is deliberately allowed while
	// a turn runs, and the approval gate reads this map from the turn-loop
	// goroutine on every gated bash command. An unguarded map read racing a
	// write is a fatal runtime error, not a benign race.
	classifierMu     sync.Mutex
	classifierModels map[string]string

	// session tool counters for the status bar (reset on SwitchSession).
	sessionToolCalls  int
	sessionToolErrors int

	// sysTokensEstimate caches the estimated token size of the system prompt
	// (base instructions + AGENTS.md + tool-name list + skills) plus the tool
	// definition schemas. buildRequest recomputes it each turn from the exact
	// text it sends; the status bar reads it (from either goroutine) via atomic
	// so the context counter reflects the whole prompt, not just messages.
	sysTokensEstimate atomic.Int64

	// expedite is set (in subagent/child mode) when the parent forwards the
	// user's Ctrl+G "finish now" nudge. The current tool call is always let to
	// finish (no hard interrupt); at the next micro-turn boundary the turn loop
	// appends expediteNudge to the last tool result AND arms
	// expediteForceNoTools, so the very next completion is forced to be the
	// final answer — the model literally cannot call another tool. Written by
	// the child's stdin-reader goroutine, read by the turn-loop goroutine —
	// hence atomic. Never set in the main agent.
	expedite atomic.Bool
	// expediteForceNoTools omits Tools from exactly one upcoming request, right
	// after an expedite nudge fires, so the model must respond with plain text
	// instead of possibly starting another tool call despite the nudge.
	expediteForceNoTools atomic.Bool

	// runTurns counts provider requests in the current run (reset each Prompt,
	// incremented per turn-loop iteration). The status bar shows it while the
	// agent works. Written by the turn-loop goroutine, read by the TUI goroutine.
	runTurns atomic.Int64

	// approvalMode gates how much of WrapRiskGatedApproval runs automatically
	// (see ApprovalMode) — Fast (default, zero value) or Paranoid. Toggled by
	// the TUI's Shift+Tab, read by the approval closure on the turn-loop
	// goroutine — hence atomic.
	approvalMode atomic.Int32

	// compactBackoffUntil suppresses auto-compaction retries after a failure.
	compactBackoffUntil time.Time

	skillsEnabled bool
	skills        []skills.Skill

	// contextMu guards loadedContextDirs and readMemos.
	contextMu sync.Mutex
	// loadedContextDirs records directories whose AGENTS.md has been injected
	// into the conversation this epoch (a file was worked on there). Each is
	// injected once; the set is reset on compaction and session switch so the
	// files are re-loaded afterwards.
	loadedContextDirs map[string]bool
	// readMemos records the last successful `read` of each path this epoch,
	// so a later re-read of the same (or a narrower) line range on an
	// unchanged file is answered with a short pointer instead of resending
	// the file — see read_memo.go. Reset on compaction and session switch:
	// a stub referencing a read that's since been summarized away would
	// dangle (the model can no longer actually see that content).
	readMemos map[string]readMemo

	// pendingInputFn lets the host (TUI) hand the turn loop a message the
	// user queued WHILE a turn was already running, so it's sent at the next
	// iteration boundary instead of only once the whole turn (which may run
	// many tool rounds, sometimes for many minutes) finally finishes. nil
	// (tests, headless/subagent use) means the loop never checks — queued
	// input, if the host has such a concept at all, is entirely the host's
	// business. Segments (not a flat string) so a queued @file reference gets
	// the same expansion a normal prompt would. Text-only otherwise: nothing
	// in this codebase queues image attachments for a message typed while a
	// turn is already in flight.
	pendingInputFn func() (segments []TextSegment, ok bool)

	// apiCallMu serializes recordAPICallCost: its seq comes from a
	// max(seq)+1 SELECT, which two concurrent recorders (parallel tool calls,
	// each able to trigger a risk classification or a web-tool helper call)
	// would otherwise read as the same number.
	apiCallMu sync.Mutex
	// cumUsageMu guards cumUsage and cumCost.
	cumUsageMu sync.Mutex
	// cumUsage is the running total of every api_calls row this Agent has
	// recorded for its session so far (main turns, compaction, and auxiliary
	// calls like "btw"/"risk" alike — recordAPICallFor accumulates into it
	// uniformly regardless of purpose). Read via CumulativeUsage(); used by
	// subagent/child mode to report live spend back to the parent process on
	// every progress tick without an extra SQL query per tick.
	cumUsage provider.Usage
	// cumCost is the matching running total in dollars, summed from each
	// row's own cost — priced against the model that served that call, which
	// the bash-risk classifier can make differ from the session model. Read
	// via CumulativeCost().
	cumCost float64
}

// SetPendingInputFn wires the callback runTurn polls at each iteration
// boundary — after a tool round, and right before it would otherwise end the
// turn — for a message queued mid-turn. See pendingInputFn's doc comment.
func (a *Agent) SetPendingInputFn(fn func() ([]TextSegment, bool)) {
	a.pendingInputFn = fn
}

// NewAgent creates an Agent ready to process prompts for the given session.
func NewAgent(
	s *store.Store,
	p provider.Provider,
	t *tools.Registry,
	cfg *config.Config,
	sessionID string,
	outputChan chan OutputEvent,
	approvalFn func(ctx context.Context, command, description, workdir string) (bool, string),
) *Agent {
	model := defaultModel(p, cfg)
	// p may legitimately be nil (e.g. risk-eval's guard-only mode, which never
	// calls the LLM) — providerID must degrade the same way defaultModel above
	// already does, or this panics on a nil-interface method call.
	providerID := ""
	if p != nil {
		providerID = p.ID()
	}
	a := &Agent{
		store:      s,
		provider:   p,
		tools:      t,
		config:     cfg,
		sessionID:  sessionID,
		outputChan: outputChan,
		approvalFn: approvalFn,
		model:      model,
		effort:     effectiveEffort(cfg, initialEffort(cfg), providerID, model),

		loadedContextDirs: map[string]bool{},
	}
	return a
}

// initialEffort resolves the starting reasoning effort from config, falling back
// to the built-in default so the status bar always shows a level.
func initialEffort(cfg *config.Config) string {
	if cfg != nil && cfg.Effort != "" {
		return cfg.Effort
	}
	return config.DefaultEffort
}

// effectiveEffort validates effort against the model's supported levels. If the
// model is known and doesn't support the requested level, the first supported
// level is used instead. Unknown models keep the effort (the provider decides).
func effectiveEffort(cfg *config.Config, effort, providerID, model string) string {
	if effort == "" {
		return ""
	}
	s, ok := provider.MergedModelSettings(cfg, providerID, model)
	if !ok {
		return effort // unknown model — keep it, provider will handle
	}
	if !s.SupportsEffort {
		return "" // known model that doesn't support effort
	}
	if len(s.EffortLevels) == 0 {
		return effort // supports effort but no specific levels listed
	}
	for _, lvl := range s.EffortLevels {
		if lvl == effort {
			return effort
		}
	}
	return s.EffortLevels[0] // not in supported list — use first supported
}

// --- Session management accessors (for TUI slash commands) ---

// Store returns the underlying store (for session/message queries).
func (a *Agent) Store() *store.Store { return a.store }

// Tools returns the session's tool registry, e.g. for looking up a specific
// tool by name and calling it directly — the same pattern
// ExpediteSubagents already uses for "subagent". The TUI's /sandbox ls and
// /sandbox kill reuse list_sandboxes'/sandbox_destroy's exact tested logic
// this way instead of duplicating it.
func (a *Agent) Tools() *tools.Registry { return a.tools }

// SessionID returns the current session ID.
func (a *Agent) SessionID() string { return a.sessionID }

// SessionToolStats returns the tool-call and tool-error counts for the current
// session run. Reset on session switch (see SwitchSession).
func (a *Agent) SessionToolStats() (calls, errors int) {
	return a.sessionToolCalls, a.sessionToolErrors
}

// SwitchSession changes the active session.
func (a *Agent) SwitchSession(sessionID string) {
	a.sessionID = sessionID
	a.sessionToolCalls = 0
	a.sessionToolErrors = 0
	a.effort = effectiveEffort(a.config, initialEffort(a.config), a.providerID(), a.model)
	a.resetContextTracker()
}

// resetContextTracker forgets which directories' AGENTS.md have been injected
// and which reads have been memoized, so both start fresh. Called on session
// switch and after compaction.
func (a *Agent) resetContextTracker() {
	a.contextMu.Lock()
	a.loadedContextDirs = map[string]bool{}
	a.readMemos = map[string]readMemo{}
	a.contextMu.Unlock()
}

// cwd resolves the working directory for the active session.
// providerID returns the active provider's ID, or "" if none is set (e.g.
// risk-eval's guard-only mode, which never touches the LLM). Every read of
// a.provider.ID() must go through this instead of calling it directly —
// a.provider can legitimately be nil, and a raw .ID() call panics.
func (a *Agent) providerID() string {
	if a.provider == nil {
		return ""
	}
	return a.provider.ID()
}

func (a *Agent) cwd() string {
	if sess, err := a.store.GetSession(a.sessionID); err == nil && sess != nil && sess.Cwd != "" {
		return sess.Cwd
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

// SetProvider swaps the provider and persists it on the active session. A
// session that isn't persisted yet is not an error (nothing to update); a
// failed write is returned so the caller can surface it.
func (a *Agent) SetProvider(p provider.Provider) error {
	a.provider = p
	a.effort = effectiveEffort(a.config, a.effort, a.providerID(), a.model)
	sess, err := a.store.GetSession(a.sessionID)
	if err != nil {
		return nil
	}
	sess.Provider = a.providerID()
	sess.UpdatedAt = time.Now().Unix()
	return a.store.UpdateSession(sess)
}

// SetModel updates the session's model name and persists it. See SetProvider
// for the error semantics.
func (a *Agent) SetModel(model string) error {
	a.model = model
	a.effort = effectiveEffort(a.config, a.effort, a.providerID(), model)
	sess, err := a.store.GetSession(a.sessionID)
	if err != nil {
		return nil
	}
	sess.Model = model
	sess.UpdatedAt = time.Now().Unix()
	return a.store.UpdateSession(sess)
}

// SetConfig swaps the config (for /reload).
func (a *Agent) SetConfig(cfg *config.Config) {
	a.config = cfg
}

// SetSkills configures skill discovery for the system prompt and skill tool.
// When enabled is false, skills are cleared and the skill tool is removed.
func (a *Agent) SetSkills(enabled bool, sk []skills.Skill) {
	a.skillsEnabled = enabled
	if !enabled || len(sk) == 0 {
		a.skills = nil
		if a.tools != nil {
			a.tools.Unregister("skill")
		}
		return
	}
	a.skills = append([]skills.Skill(nil), sk...)
	if a.tools != nil {
		a.tools.Register(tools.NewSkillTool(a.skills))
	}
}

// SkillsEnabled reports whether skill loading is active for this process.
func (a *Agent) SkillsEnabled() bool { return a.skillsEnabled }

// Skills returns the current skill list (may be nil).
func (a *Agent) Skills() []skills.Skill {
	if len(a.skills) == 0 {
		return nil
	}
	return append([]skills.Skill(nil), a.skills...)
}

// ReloadSkills rediscovers ~/.poisson/skills and refreshes prompt + skill tool.
func (a *Agent) ReloadSkills() (int, error) {
	if !a.skillsEnabled {
		a.skills = nil
		if a.tools != nil {
			a.tools.Unregister("skill")
		}
		return 0, nil
	}
	sk, err := skills.Discover()
	if err != nil {
		return 0, err
	}
	a.SetSkills(true, sk)
	return len(sk), nil
}

// ReloadConfigDependentTools updates tools gated on the active provider and
// runtime config: fetch's backends (curl / ollama / anthropic) and
// web_search's optional Anthropic backend. fetch is always registered — it
// used to be Ollama-only (unregistered entirely on every other provider),
// which meant Anthropic/OpenAI/xAI sessions never had a working fetch tool at
// all. Ollama's own web_fetch API is offered when it's the active provider and
// reachable (its own extraction, already good); Anthropic's server-side search
// and small-model page summarizer are offered when Anthropic is active, since
// they spend that account's tokens. Every provider switch must call this, or a
// backend stays wired to a provider the session already left.
func (a *Agent) ReloadConfigDependentTools() {
	if a.tools == nil || a.config == nil {
		return
	}
	ollamaBaseURL := ""
	if a.providerID() == "ollama" && tools.IsOllamaReachable(a.config) {
		ollamaBaseURL = tools.OllamaBaseURL(a.config)
	}
	// Typed nil check, not a plain assignment: assigning a nil
	// *AnthropicProvider into the interface yields a non-nil interface, which
	// would advertise the Anthropic backends on every provider.
	var anthropicWeb tools.AnthropicWebBackend
	if ap, ok := a.provider.(*provider.AnthropicProvider); ok && ap != nil {
		anthropicWeb = ap
	}
	a.tools.Register(tools.NewFetchTool(ollamaBaseURL, anthropicWeb))
	a.tools.Register(tools.NewWebSearchTool(anthropicWeb))
	// Re-applied after every Register: a freshly built tool has no sink, and
	// web_ask (registered once, by BuildRegistry) needs one too. Without it a
	// web tool's helper-model spend goes unrecorded — see RecordWebToolCall.
	tools.BindWebUsage(a.tools, a.RecordWebToolCall)
}

// Provider returns the current provider.
func (a *Agent) Provider() provider.Provider { return a.provider }

// AnthropicUsageLimits returns the last cached Anthropic 5h/7-day usage
// snapshot, or nil if the current provider isn't Anthropic or nothing has
// been fetched yet. Never makes a network call — safe to call from a
// render/status-sync path.
func (a *Agent) AnthropicUsageLimits() *provider.AnthropicUsageLimits {
	ap, ok := a.provider.(*provider.AnthropicProvider)
	if !ok {
		return nil
	}
	return ap.CachedUsageLimits()
}

// RefreshAnthropicUsageLimits fetches fresh usage data, a no-op unless the
// current provider is Anthropic. The provider's own UsageLimits enforces a
// 5-minute TTL internally, so calling this often (e.g. from a ticker) is
// harmless — most calls just return the cache without a network round trip.
// Errors are swallowed by design: this drives a background status-bar
// refresh, not a user-facing operation with something to report failure to.
func (a *Agent) RefreshAnthropicUsageLimits(ctx context.Context) {
	ap, ok := a.provider.(*provider.AnthropicProvider)
	if !ok {
		return
	}
	_, _ = ap.UsageLimits(ctx)
}

// RefreshAnthropicUsageLimitsForce is RefreshAnthropicUsageLimits but bypasses
// the TTL — used right after a provider/session switch, where the header
// should show guaranteed-current data immediately rather than whatever
// happens to already be cached (or nothing, until the next scheduled tick).
func (a *Agent) RefreshAnthropicUsageLimitsForce(ctx context.Context) {
	ap, ok := a.provider.(*provider.AnthropicProvider)
	if !ok {
		return
	}
	_, _ = ap.ForceUsageRefresh(ctx)
}

// OpenAIUsageLimits returns the last cached Codex usage snapshot, or nil if
// the current provider isn't OpenAI or nothing has been fetched yet. Never
// makes a network call — safe to call from a render/status-sync path.
func (a *Agent) OpenAIUsageLimits() *provider.CodexUsage {
	op, ok := a.provider.(*provider.OpenAIProvider)
	if !ok {
		return nil
	}
	return op.CachedUsageLimits()
}

// RefreshOpenAIUsageLimits fetches fresh Codex usage data, a no-op unless
// the current provider is OpenAI. Same TTL/error-swallowing reasoning as
// RefreshAnthropicUsageLimits above.
func (a *Agent) RefreshOpenAIUsageLimits(ctx context.Context) {
	op, ok := a.provider.(*provider.OpenAIProvider)
	if !ok {
		return
	}
	_, _ = op.UsageLimits(ctx)
}

// RefreshOpenAIUsageLimitsForce is RefreshOpenAIUsageLimits but bypasses the
// TTL — see RefreshAnthropicUsageLimitsForce for the reasoning.
func (a *Agent) RefreshOpenAIUsageLimitsForce(ctx context.Context) {
	op, ok := a.provider.(*provider.OpenAIProvider)
	if !ok {
		return
	}
	_, _ = op.ForceUsageRefresh(ctx)
}

// ResetOpenAIUsage spends one of the account's free Codex "reset this usage
// window early" credits. Unlike the Refresh* methods above, this is a
// direct user-triggered action (the /openai-reset-usage command) — its
// error IS surfaced, not swallowed, since there's a human waiting to know
// whether it worked.
func (a *Agent) ResetOpenAIUsage(ctx context.Context) (*provider.CodexResetResult, error) {
	op, ok := a.provider.(*provider.OpenAIProvider)
	if !ok {
		return nil, fmt.Errorf("resetting usage is only available with the OpenAI/Codex provider")
	}
	return op.ResetUsage(ctx)
}

// Config returns the current config.
func (a *Agent) Config() *config.Config { return a.config }

// contextInjectionForFile returns the AGENTS.md/CLAUDE.md content to append to a
// tool result when a file was worked on (read/edit/write), loading each
// applicable file at most once per epoch. When cwd is an ancestor of the file's
// directory, the whole chain from cwd down to that directory is considered;
// otherwise only the file's own directory is. Files already carried by the
// system prompt (sysPaths: global + cwd) are never re-injected.
func (a *Agent) contextInjectionForFile(cwd, toolName string, input json.RawMessage, sysPaths map[string]bool) string {
	if !isFileTool(toolName) {
		return ""
	}
	var in struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(input, &in) != nil || in.Path == "" {
		return ""
	}
	p := in.Path
	if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	dirs := project.ContextDirsForFile(cwd, filepath.Dir(p))

	var out strings.Builder
	a.contextMu.Lock()
	defer a.contextMu.Unlock()
	if a.loadedContextDirs == nil {
		a.loadedContextDirs = map[string]bool{}
	}
	for _, d := range dirs {
		if a.loadedContextDirs[d] {
			continue
		}
		cf := project.ContextFileInDir(d)
		if cf == nil || sysPaths[cf.Path] {
			continue
		}
		a.loadedContextDirs[d] = true
		out.WriteString("\n\n<project_instructions path=\"")
		out.WriteString(cf.Path)
		out.WriteString("\">\n")
		out.WriteString(cf.Content)
		out.WriteString("\n</project_instructions>")
	}
	return out.String()
}

// systemPromptContextPaths returns the set of AGENTS.md/CLAUDE.md paths carried
// by the system prompt (global + cwd), which must never be re-injected.
func (a *Agent) systemPromptContextPaths(cwd string) map[string]bool {
	paths := map[string]bool{}
	for _, cf := range project.LoadProjectContextFiles(cwd, config.ConfigDir(), nil) {
		paths[cf.Path] = true
	}
	return paths
}

// LoadedContextFiles returns every AGENTS.md/CLAUDE.md currently in the
// session's context: the system-prompt ones (global + cwd) plus each directory
// whose file has been injected this epoch. Used by /status.
func (a *Agent) LoadedContextFiles() []project.ContextFile {
	cwd := a.cwd()
	a.contextMu.Lock()
	dirs := make([]string, 0, len(a.loadedContextDirs))
	for d := range a.loadedContextDirs {
		dirs = append(dirs, d)
	}
	a.contextMu.Unlock()
	return project.LoadProjectContextFiles(cwd, config.ConfigDir(), dirs)
}

// ToolNames returns the sorted names of the currently registered tools.
func (a *Agent) ToolNames() []string {
	if a.tools == nil {
		return nil
	}
	defs := a.tools.Definitions()
	names := make([]string, 0, len(defs))
	for _, td := range defs {
		names = append(names, td.Name)
	}
	sort.Strings(names)
	return names
}

// SetEffort sets the thinking effort for subsequent requests.
func (a *Agent) SetEffort(level string) { a.effort = level }

// Model returns the current model name.
func (a *Agent) Model() string {
	if a.model != "" {
		return a.model
	}
	return defaultModel(a.provider, a.config)
}

// defaultModel returns the configured default for a given provider, reading
// from the single config.Providers registry so a new provider (e.g.
// llamacpp) doesn't need a second hand-copied switch here.
func defaultModel(p provider.Provider, cfg *config.Config) string {
	if cfg == nil || p == nil {
		return ""
	}
	return provider.DefaultModel(p.ID(), cfg)
}

// Effort returns the current thinking effort level.
func (a *Agent) Effort() string { return a.effort }

// Expedite marks this agent to wrap up early. A subagent child sets it when the
// parent forwards the user's Ctrl+G nudge; the turn loop then injects a
// finish-now message at the next micro-turn boundary. No-op in the main agent.
func (a *Agent) Expedite() { a.expedite.Store(true) }

// RunTurns returns the number of provider requests made in the current run.
func (a *Agent) RunTurns() int { return int(a.runTurns.Load()) }

// SendSubagentProgress emits a live turn-count + context-usage update for a
// running subagent widget. toolCallID correlates to the OutputToolStart that
// created it (see tools.WithToolCallID). tokensPerSec is the child's own
// token-weighted average inference speed across the rounds it has completed
// so far, accumulated by SubagentTool.Execute (0 if it hasn't reported one
// yet — see agent.OutputInferenceSpeed and subagent.ChildEvent). status is
// normally "" (ordinary progress); when the child is mid-network-retry it
// carries a short human-readable status ("connection lost: ... —
// reconnecting…" / "reconnected — resuming") for the widget to show in place
// of its turn/context line. Called from the subagent tool's own goroutine
// while its child is still running, concurrently with the rest of the turn
// loop — sendEvent is the same channel-send already used for tool_result
// from that goroutine, so this is safe.
func (a *Agent) SendSubagentProgress(toolCallID string, turns, contextTokens, contextWindow int, tokensPerSec float64, status string) {
	a.sendEvent(OutputEvent{
		Type: OutputSubagentProgress, ToolCallID: toolCallID, SubagentTurns: turns,
		ContextTokens: contextTokens, ContextWindow: contextWindow,
		SubagentTokensPerSec: tokensPerSec, Text: status,
	})
}

// CompleteBatchedSubagent reports a batched subagent call's completion to
// the TUI (see tools.BindBatchSubagentDone) — the counterpart to the
// OutputToolResult a direct (non-batched) subagent call already gets from
// the ordinary per-call dispatch path in runTurn. Called from batch's own
// goroutine while other calls in the same batch may still be running,
// concurrently with the rest of the turn loop — same as SendSubagentProgress,
// sendEvent is safe for that.
func (a *Agent) CompleteBatchedSubagent(toolCallID string, res tools.ToolResult) {
	a.sendEvent(OutputEvent{
		Type: OutputToolResult, ToolName: "subagent", ToolCallID: toolCallID,
		ToolResultContent: res.Content, ToolError: res.Error,
	})
}

// ExpediteSubagents forwards the user's "finish now" nudge to every running
// subagent child and returns how many were signalled. The main agent's own
// turn is left untouched. Used by the TUI Ctrl+G handler.
func (a *Agent) ExpediteSubagents() int {
	if a.tools == nil {
		return 0
	}
	t, ok := a.tools.Get("subagent")
	if !ok {
		return 0
	}
	st, ok := t.(*tools.SubagentTool)
	if !ok {
		return 0
	}
	return st.ExpediteAll()
}

// EnsureSession persists the active session row if it does not exist yet.
// Sessions are created lazily on the first user message, not at process start.
func (a *Agent) EnsureSession() error {
	_, err := a.store.GetSession(a.sessionID)
	if err == nil {
		return nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	cwd, _ := os.Getwd()
	now := time.Now().Unix()
	return a.store.CreateSession(&store.Session{
		ID:        a.sessionID,
		Cwd:       cwd,
		Provider:  a.providerID(),
		Model:     a.Model(),
		CreatedAt: now,
		UpdatedAt: now,
	})
}

// ImageAttachment is an already-processed image (downscaled, on disk) to send
// with a user message. Name is the original filename (e.g. "screenshot.png",
// or "clipboard" for a paste) — display metadata carried through to
// provider.ContentBlock.ImageName so it survives a session resume; Path is
// always a random /tmp basename, never the name the user actually typed.
type ImageAttachment struct {
	Path      string
	MediaType string
	Name      string
}

// TextSegment is one piece of a user message's text: either something the
// user typed directly, or a file's contents inlined via an @path reference
// (FileRef holds the source path, empty for plain typed text). Splitting a
// message into segments — rather than one flat string — preserves enough
// structure for the TUI to redraw a file segment as a collapsible card on
// resume instead of dumping its content inline forever; the model still sees
// the exact same text, just spread across adjacent text blocks.
type TextSegment struct {
	Text    string
	FileRef string
}

// Prompt appends the user message to the store and runs the turn loop.
func (a *Agent) Prompt(userInput string) error {
	return a.PromptWithContext(context.Background(), userInput)
}

// PromptWithContext is Prompt with cancellation support. Any images are sent as
// image content blocks alongside the text in the user message.
func (a *Agent) PromptWithContext(ctx context.Context, userInput string, images ...ImageAttachment) error {
	return a.PromptSegmentsWithContext(ctx, []TextSegment{{Text: userInput}}, images...)
}

// PromptSegments is PromptSegmentsWithContext using a background context.
func (a *Agent) PromptSegments(segments []TextSegment, images ...ImageAttachment) error {
	return a.PromptSegmentsWithContext(context.Background(), segments, images...)
}

// PromptSegmentsWithContext is PromptWithContext for a message split into
// segments (see TextSegment) instead of one flat string.
func (a *Agent) PromptSegmentsWithContext(ctx context.Context, segments []TextSegment, images ...ImageAttachment) error {
	if err := a.EnsureSession(); err != nil {
		return a.failTurn(fmt.Sprintf("Session error: %v", err), fmt.Errorf("ensure session: %w", err))
	}

	// A prior process may have died mid tool-round (killed, crashed, machine
	// lost power) leaving the last assistant message's tool_use blocks
	// without matching tool_result rows in the store. Repair that before
	// appending the new user message, while the dangling tool_use is still
	// trailing — every provider rejects a request whose history has a
	// tool_use without a following tool_result, so an unrepaired resume
	// fails every time with a 400. Safe to call here (and nowhere else):
	// this is the sole entry point that starts a new turn, so there is
	// never a legitimately still-running tool call to mistake for a dead
	// one (contrast quickanswer's buildRequest call mid-turn, which must
	// NOT trigger this — see pendingToolResultBlocks).
	if err := a.repairDanglingToolUse(); err != nil {
		return a.failTurn(fmt.Sprintf("Session error: %v", err), fmt.Errorf("repair dangling tool_use: %w", err))
	}

	// INGEST: append user message (images first, then the text segments).
	if err := a.appendUserMessage(segments, images); err != nil {
		return a.failTurn(err.Error(), err)
	}

	a.runTurns.Store(0)
	err := a.runTurn(ctx)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		// Keep the conversation visible — just stop generation. runTurn's
		// persistPartialTurnOnCancel already saved whatever text/thinking (and
		// any complete tool_use) had streamed before the cancel, matching what
		// the user actually saw, so the next turn sees it too. Previous tool
		// iterations (if any) already have complete tool_use+result pairs.
	}
	return err
}

// expediteNudge is appended to the last tool result when the user asks a
// subagent to finish early (Ctrl+G). It rides inside the tool-result (user)
// turn, so it never creates consecutive user messages some providers reject.
// A model occasionally returns a complete-but-empty response (no text,
// thinking, or tool calls) — a transient provider glitch, seen most with
// Anthropic. runTurn retries the same request up to maxEmptyResponseRetries
// times (Nth retry waits N × emptyResponseBackoff) before surfacing an error.
const maxEmptyResponseRetries = 3

// maxTurnContinuations bounds how many times a single turn may auto-continue
// after being cut off by the provider's output-token cap (stop_reason=max_tokens).
const maxTurnContinuations = 8

// maxConcurrentToolCalls bounds how many tool_use blocks from one model
// response run at once — see the dispatch loop in runTurn.
const maxConcurrentToolCalls = 8

// approvalGatedTools are tool names whose Execute may ask a human for
// approval: bash's risk gate, edit/write/sandbox_cp's sensitive-path gate,
// create_sandbox's mount/env gate, and subagent's relayed child approvals.
// Every one of these funnels through the same single-flight TUI.Approve
// call (see its doc comment), which has no ordering guarantee across
// concurrent goroutines — a plain sync.Mutex hands out its lock in
// whatever order the Go scheduler happens to wake blocked goroutines, not
// necessarily the model's submission order. Two gated calls dispatched
// concurrently can therefore show their approval prompts (and run) out of
// order — e.g. a `create_sandbox` call submitted after a `bash` call
// prompting/finishing first, even though the sandbox may depend on the
// bash command. The dispatch loop below pulls these out of the concurrent
// pool and runs them one at a time, in submission order, so two of them can
// never be in flight together — the race is structurally impossible rather
// than merely unlikely. Every other tool call keeps full concurrency.
var approvalGatedTools = map[string]bool{
	"bash":           true,
	"edit":           true,
	"write":          true,
	"subagent":       true,
	"create_sandbox": true,
	"sandbox_cp":     true,
}

// isGatedCall reports whether tc must go through the sequential gated
// walker rather than the concurrent pool: either its own name is directly
// gated, or (batch only) it wraps at least one nested call whose tool is.
// batch.go's own mutatingTools set independently forces any such batch to
// run its nested calls serially once Execute starts — this check is what
// keeps that same batch call from itself racing another top-level gated
// call (e.g. a plain `bash` submitted alongside a `batch{create_sandbox}`)
// through the two different dispatch paths simultaneously. Without it, the
// top-level partitioning below only ever sees "batch", never what's inside,
// and the ordering guarantee approvalGatedTools exists for would hold
// within a batch but not across the batch/non-batch boundary.
func isGatedCall(tc tools.ToolCall) bool {
	if approvalGatedTools[tc.Name] {
		return true
	}
	if tc.Name != "batch" {
		return false
	}
	for _, spec := range tools.ParseBatchCalls(tc.Input) {
		// ParseBatchCalls only strips the wire prefix (mcp_Bash -> Bash),
		// it doesn't apply the registry's final full-string lowercase
		// fallback — CanonicalToolName does, matching what Registry.Canonical
		// would actually resolve a half-stripped name like "Bash" to, so an
		// oddly-cased nested tool name can't slip past this check unrecognized.
		if approvalGatedTools[tools.CanonicalToolName(spec.Tool)] {
			return true
		}
	}
	return false
}

// emptyResponseBackoff is a var so tests can shorten it.
var emptyResponseBackoff = 500 * time.Millisecond

// maxMidStreamErrorRetries bounds how many times runTurn retries a round
// that failed with a Retryable mid-stream provider error (e.g. Anthropic's
// overloaded_error arriving as an SSE event after the response already
// started with HTTP 200 — DoWithRetry's pre-stream retry never sees these).
// Only retried when nothing has been streamed to the user yet this round;
// see the EventError case in runTurn.
const maxMidStreamErrorRetries = 3

// midStreamErrorBackoff is a var so tests can shorten it.
var midStreamErrorBackoff = 1 * time.Second

// minInferenceSpeedElapsed floors how short a round's wall-clock duration may
// be before sendInferenceSpeedEvent bothers reporting a tok/s figure for it.
// Below this, timer granularity and request/response overhead dominate the
// measurement — dividing a handful of tokens by a near-zero duration produces
// a wildly inflated number, not a meaningful speed reading. A var (like
// emptyResponseBackoff/midStreamErrorBackoff above) so tests can shrink it
// instead of needing a real sleep to clear the floor.
var minInferenceSpeedElapsed = 100 * time.Millisecond

const expediteNudge = "\n\n[User interjection] The user needs results now and has asked you to wrap up immediately. Stop starting new work: summarize what you have accomplished so far — partial results are fine — and finish this turn without any further tool calls."

// appendUserMessage builds the content blocks for a user turn (images first,
// then text segments) and appends it to the store. Shared by
// PromptSegmentsWithContext (the message that starts a turn) and
// injectPendingInput (a message queued while a turn is already running).
func (a *Agent) appendUserMessage(segments []TextSegment, images []ImageAttachment) error {
	var blocks []provider.ContentBlock
	for _, im := range images {
		if im.Path == "" {
			continue
		}
		mt := im.MediaType
		if mt == "" {
			mt = "image/png"
		}
		blocks = append(blocks, provider.ContentBlock{Type: "image", MediaType: mt, ImagePath: im.Path, ImageName: im.Name})
	}
	textBlocks := 0
	for _, seg := range segments {
		if seg.Text == "" && seg.FileRef == "" {
			continue
		}
		blocks = append(blocks, provider.ContentBlock{Type: "text", Text: seg.Text, FileRef: seg.FileRef})
		textBlocks++
	}
	if textBlocks == 0 && len(blocks) == 0 {
		blocks = append(blocks, provider.ContentBlock{Type: "text"})
	}
	content, err := contentBlocksToJSON(blocks)
	if err != nil {
		return fmt.Errorf("marshal user content: %w", err)
	}
	return a.store.AppendMessage(&store.Message{
		SessionID: a.sessionID,
		Role:      "user",
		Content:   content,
	})
}

// injectPendingInput polls pendingInputFn and, if a message is queued,
// appends it as a fresh user turn — same shape as appendContinueMessage.
// Only safe to call where the previous message was plain assistant text (no
// tool_use): a bare user-role row right after an assistant text reply keeps
// roles alternating. The tool-round case (assistant tool_use → tool results)
// is handled separately, in runTurn's dispatch loop, by folding queued text
// into the last tool_result instead of appending a whole new row — a fresh
// user-role row there would follow the coalesced tool-result "user" message
// and produce two consecutive user-role messages at the wire level, which
// Anthropic rejects. See the comment at that call site.
func (a *Agent) injectPendingInput() (bool, error) {
	if a.pendingInputFn == nil {
		return false, nil
	}
	segments, ok := a.pendingInputFn()
	if !ok {
		return false, nil
	}
	return true, a.appendUserMessage(segments, nil)
}

// flattenSegments joins segments' text (an @file reference's expanded
// content is already inlined into its segment's Text by the time this is
// called) into one plain string — used where the destination is a flat
// string field (a tool_result), not a content-block array.
func flattenSegments(segments []TextSegment) string {
	var b strings.Builder
	for i, s := range segments {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(s.Text)
	}
	return b.String()
}

// appendContinueMessage adds a synthetic user turn asking the model to resume
// after its previous response was truncated by the output-token cap. A user
// turn (rather than a second assistant message) keeps roles alternating, which
// Anthropic requires.
func (a *Agent) appendContinueMessage() error {
	content, err := contentBlocksToJSON([]provider.ContentBlock{
		{Type: "text", Text: "Continue exactly where you left off. Do not repeat what you already wrote."},
	})
	if err != nil {
		return err
	}
	return a.store.AppendMessage(&store.Message{
		SessionID: a.sessionID,
		Role:      "user",
		Content:   content,
	})
}

// runTurn executes the turn loop: build → stream → collect tools → dispatch →
// append results → check compaction → repeat until no tool calls.
// streamWithRetryNotice calls a.provider.Stream with a provider.RetryTrace
// attached to ctx, translating backoff retries (connection failures,
// transient 5xx/429) into at most two OutputRetrying events for this one
// call: one when a fresh outage starts (the first retry), one when it
// recovers. Never once per attempt — an outage that takes many retries to
// clear must not spam the conversation with a line per attempt.
func (a *Agent) streamWithRetryNotice(ctx context.Context, req *provider.Request) (<-chan provider.StreamEvent, error) {
	// Preserve MaxElapsed from any trace the caller already attached (e.g.
	// cmd/px/main.go's child mode bounding a subagent's retry budget) —
	// WithRetryTrace replaces whatever trace is on ctx, so building a new one
	// from scratch here would silently drop that budget.
	var maxElapsed time.Duration
	if existing := provider.RetryTraceFromContext(ctx); existing != nil {
		maxElapsed = existing.MaxElapsed
	}
	trace := &provider.RetryTrace{
		MaxElapsed: maxElapsed,
		OnRetry: func(attempt int, delay time.Duration, reason string) {
			if attempt == 1 {
				a.sendEvent(OutputEvent{Type: OutputRetrying, Text: fmt.Sprintf("connection lost: %s — reconnecting…", reason)})
			}
		},
		OnRecovered: func() {
			a.sendEvent(OutputEvent{Type: OutputRetrying, Text: "reconnected — resuming"})
		},
	}
	return a.provider.Stream(provider.WithRetryTrace(ctx, trace), req)
}

func (a *Agent) runTurn(ctx context.Context) error {
	emptyAttempts := 0
	continuations := 0
	midStreamRetries := 0
roundLoop:
	for {
		if err := ctx.Err(); err != nil {
			return a.endTurn(err)
		}

		// Compact before building every request. A model switch can reduce the
		// context window below the active conversation size; waiting until after
		// a tool round would send one already-oversized request first.
		if _, err := a.store.GetLastAPICall(a.sessionID); err == nil && a.shouldCompact() {
			if err := a.compact(ctx, true, true); err != nil {
				if !errors.Is(err, ErrNothingToCompact) {
					log.Printf("warning: auto-compaction failed: %v", err)
				}
				a.compactBackoffUntil = time.Now().Add(90 * time.Second)
			}
		}

		a.runTurns.Add(1)
		// BUILD
		req, err := a.buildRequest()
		if err != nil {
			return a.failTurn(fmt.Sprintf("Build error: %v", err), fmt.Errorf("build request: %w", err))
		}
		if a.expediteForceNoTools.Swap(false) {
			// Hard stop: no tools means no possible tool_use block, so this
			// completion must be the final text answer.
			req.Tools = nil
		}

		// CALL — streamCtx is scoped to this one round: cancelling it on every
		// exit below (not just via the caller's ctx) is what lets the provider's
		// pump goroutine unblock and close its HTTP body if this loop ever stops
		// draining ch before the pump is done sending on it (see EventError
		// below). Without a per-round cancel, ctx here is context.Background()
		// on the interactive REPL's main turn loop, so a pump left blocked on a
		// send has no way to ever unblock.
		streamCtx, cancel := context.WithCancel(ctx)
		roundStart := time.Now()
		ch, err := a.streamWithRetryNotice(streamCtx, req)
		if err != nil {
			cancel()
			if ctx.Err() != nil {
				// Cancelled (e.g. user Ctrl+C) while connecting or mid-backoff
				// retry — same silent shape as every other ctx-cancellation exit
				// in this loop, not a user-facing error.
				return a.endTurn(err)
			}
			return a.failTurn(fmt.Sprintf("Provider error: %v", err), fmt.Errorf("stream: %w", err))
		}

		// Drain the stream channel.
		var textBuilder strings.Builder
		var thinkingBuilder strings.Builder
		var thinkingSig strings.Builder
		var redactedThinking []provider.ContentBlock
		var toolCalls []provider.ToolCall
		var usage *provider.Usage
		var stopReason string

		for ev := range ch {
			switch ev.Type {
			case provider.EventTextDelta:
				textBuilder.WriteString(ev.Text)
				a.sendEvent(OutputEvent{Type: OutputText, Text: ev.Text})

			case provider.EventThinkingDelta:
				thinkingBuilder.WriteString(ev.Text)
				a.sendEvent(OutputEvent{Type: OutputThinking, Text: ev.Text})

			case provider.EventThinkingSignature:
				thinkingSig.WriteString(ev.Text)

			case provider.EventThinkingRedacted:
				redactedThinking = append(redactedThinking, provider.ContentBlock{
					Type: "thinking", Redacted: true, ThinkingSignature: ev.Text,
				})
				a.sendEvent(OutputEvent{Type: OutputThinking, ThinkingRedacted: true})

			case provider.EventToolUseStart:
				if ev.ToolCall != nil {
					tc := *ev.ToolCall
					tc.Name = a.canonicalToolName(tc.Name)
					toolCalls = append(toolCalls, tc)
				}

			case provider.EventToolUseDelta:
				a.updateToolCall(toolCalls, ev.ToolCall, false)

			case provider.EventToolUseStop:
				a.updateToolCall(toolCalls, ev.ToolCall, true)

			case provider.EventDone:
				usage = ev.Usage
				stopReason = ev.StopReason

			case provider.EventError:
				// Cancel before returning: this bails out of `range ch` while the
				// pump goroutine may still be running, so its next (or fallback)
				// send on ch must see streamCtx cancelled to take its ctx.Done()
				// escape instead of blocking forever with no reader left.
				cancel()
				// A Retryable error (provider-side capacity/load problem, not a
				// bad request) is worth retrying the whole round for — but only
				// if nothing has reached the user yet this round. Once any text,
				// thinking, or tool-call content has streamed out, retrying would
				// re-send it from scratch and duplicate what's already visible;
				// safer to fail like any other mid-stream error at that point.
				// The decision itself is shared with the one-shot auxiliary-call
				// path (streamAndCollect) — see stream_retry.go.
				noContentYet := textBuilder.Len() == 0 && thinkingBuilder.Len() == 0 &&
					len(toolCalls) == 0 && len(redactedThinking) == 0
				if shouldRetryMidStream(ev, noContentYet, midStreamRetries) {
					midStreamRetries++
					a.sendEvent(OutputEvent{Type: OutputRetrying, Text: fmt.Sprintf(
						"provider overloaded: %s — retrying (%d/%d)…", ev.Error, midStreamRetries, maxMidStreamErrorRetries)})
					if err := sleepOrDone(ctx, midStreamRetryDelay(midStreamRetries)); err != nil {
						return a.endTurn(err)
					}
					continue roundLoop
				}
				return a.failTurn(ev.Error.Error(), fmt.Errorf("stream error: %w", ev.Error))
			}
		}
		// Stream drained to completion (channel closed by the pump) — release
		// this round's context promptly rather than waiting for runTurn to
		// eventually return, since a turn can loop over many rounds.
		cancel()

		// A model that leaves "provider" out of a web_ask/web_search/fetch
		// call (or one nested inside batch) still runs on SOME backend —
		// resolve it now for persistence and the TUI, so a card shows the
		// backend that actually ran instead of an empty field that reads as
		// "no backend" when it really means "whichever one is default right
		// now". This is a DISPLAY-only copy, deliberately never fed to
		// Execute below: some tools tell an explicit "provider" the model
		// typed apart from an auto-picked default by literally checking
		// whether their own input JSON's "provider" field is already set
		// (web_ask falls back grok -> exa only when it self-selected grok as
		// the default; an explicit provider=grok request instead surfaces
		// the real error). Rewriting toolCalls[i].Input itself before
		// dispatch would erase that distinction — Execute would see
		// "provider":"grok" and treat every resolved-default call as if the
		// model had demanded grok specifically, silently turning a
		// graceful exa fallback into a hard failure on any transient xAI
		// hiccup. So the dispatch loop further down still runs on the
		// original, unresolved toolCalls.
		displayToolCalls := toolCalls
		if a.tools != nil {
			displayToolCalls = make([]provider.ToolCall, len(toolCalls))
			for i, tc := range toolCalls {
				tc.Input = tools.InjectResolvedProviders(a.tools, tc.Name, tc.Input)
				displayToolCalls[i] = tc
			}
		}

		if err := ctx.Err(); err != nil {
			a.persistPartialTurnOnCancel(textBuilder.String(), thinkingBuilder.String(), thinkingSig.String(), redactedThinking, displayToolCalls)
			return a.endTurn(err)
		}

		// COMMIT: record api_call (exact usage + cost).
		var apiCallID string
		if usage != nil {
			id, err := a.recordAPICall(usage)
			if err != nil {
				log.Printf("warning: record api call: %v", err)
			} else {
				apiCallID = id
			}
		}

		// COMMIT: append assistant message.
		assistantBlocks := buildAssistantBlocks(
			thinkingBuilder.String(), thinkingSig.String(), redactedThinking,
			textBuilder.String(), displayToolCalls)
		if len(assistantBlocks) == 0 {
			// Model returned nothing (no text, thinking, or tool calls). This is
			// a transient provider glitch (notably Anthropic), so retry the same
			// request a few times before giving up — erroring out here would
			// strand the turn and force the user to re-prompt, leaving two
			// consecutive user messages in history. Don't persist the empty
			// message: an empty content array is a provider 400 next turn.
			if emptyAttempts < maxEmptyResponseRetries {
				emptyAttempts++
				a.sendEvent(OutputEvent{Type: OutputError, Text: fmt.Sprintf(
					"empty response from model — retrying (%d/%d)", emptyAttempts, maxEmptyResponseRetries)})
				if err := sleepOrDone(ctx, emptyResponseRetryDelay(emptyAttempts)); err != nil {
					return a.endTurn(err)
				}
				continue
			}
			return a.failTurn("model returned no content", fmt.Errorf("model returned empty response"))
		}
		emptyAttempts = 0
		midStreamRetries = 0
		assistantContent, err := contentBlocksToJSON(assistantBlocks)
		if err != nil {
			return a.failTurn(fmt.Sprintf("Marshal error: %v", err), fmt.Errorf("marshal assistant content: %w", err))
		}
		msg := &store.Message{
			SessionID: a.sessionID,
			Role:      "assistant",
			Content:   assistantContent,
		}
		if apiCallID != "" {
			msg.APICallID = &apiCallID
		}
		if err := a.store.AppendMessage(msg); err != nil {
			return a.failTurn(fmt.Sprintf("Store error: %v", err), fmt.Errorf("append assistant message: %w", err))
		}

		// Update status bar.
		a.UpdateStatus()

		// If the model didn't call any tools, the turn is normally done — unless
		// it was cut off by the output-token cap mid-answer, in which case we keep
		// going. A synthetic user turn (rather than a second assistant message)
		// preserves role alternation, which Anthropic requires. Bounded to avoid
		// an unbounded loop if the model keeps hitting the cap.
		if len(toolCalls) == 0 {
			// No tool calls this round — every block it produced (thinking,
			// answer text) already exists in the TUI, so it's safe to report
			// this round's speed now, before whichever exit path below runs.
			a.sendInferenceSpeedEvent(usage, roundStart)
			if stopReason == "max_tokens" && continuations < maxTurnContinuations {
				continuations++
				a.sendEvent(OutputEvent{Type: OutputError, Text: fmt.Sprintf(
					"response hit the output limit — continuing (%d/%d)", continuations, maxTurnContinuations)})
				if err := a.appendContinueMessage(); err != nil {
					return a.failTurn(fmt.Sprintf("Store error: %v", err), fmt.Errorf("append continue message: %w", err))
				}
				continue
			}
			// The model is about to give its final answer and stop — but if the
			// user queued a message while this turn was running, don't end yet:
			// splice it in as the next user turn and keep going, so it's
			// answered now instead of requiring a fresh prompt afterward.
			if injected, err := a.injectPendingInput(); err != nil {
				return a.failTurn(fmt.Sprintf("Store error: %v", err), fmt.Errorf("append queued message: %w", err))
			} else if injected {
				continue
			}
			a.sendEvent(OutputEvent{Type: OutputDone})
			break
		}

		// TOOLS: notify TUI of tool starts. Uses displayToolCalls (resolved
		// providers) — the card the user sees, not the raw dispatch input.
		for _, tc := range displayToolCalls {
			a.sendEvent(OutputEvent{
				Type:       OutputToolStart,
				ToolName:   tc.Name,
				ToolCallID: tc.ID,
				ToolInput:  tc.Input,
			})
			// A subagent nested inside a batch call otherwise never gets its
			// own start/progress/done events — those normally come from the
			// per-call dispatch path below, which batch bypasses by running
			// its nested calls internally (see tools.BatchTool.Execute). Pre-
			// render one widget per nested subagent here, keyed by the same
			// synthetic ID batch.go threads into that call's context, so the
			// TUI's existing subagent-widget handling (already keyed only on
			// ToolName=="subagent") picks it up with no changes on that side.
			if tc.Name == "batch" {
				for i, spec := range tools.ParseBatchCalls(tc.Input) {
					if spec.Tool != "subagent" {
						continue
					}
					a.sendEvent(OutputEvent{
						Type:       OutputToolStart,
						ToolName:   "subagent",
						ToolCallID: tools.BatchCallID(tc.ID, i),
						ToolInput:  spec.Input,
					})
				}
			}
		}
		// Report this round's speed only now that the tool-call blocks it
		// produced exist in the TUI too (thinking/text blocks, if any,
		// already existed — they streamed live during the round above).
		a.sendInferenceSpeedEvent(usage, roundStart)

		// Dispatch concurrently; emit each tool_result to the TUI as it finishes
		// so cards stop spinning without waiting for slower siblings (e.g. bash approval).
		//
		// approvalGatedTools calls are pulled out of this concurrent pool and
		// run one at a time, in the model's own submission order, by the
		// sequential walker below instead — see approvalGatedTools' doc
		// comment for why. Every other call keeps full concurrency exactly
		// as before.
		dispatchCwd := a.cwd()
		results := make([]tools.ToolResult, len(toolCalls))
		var wg sync.WaitGroup
		// Bounds how many of this round's tool calls run at once — a model
		// response with an unusually large number of parallel tool_use
		// blocks (buggy, or steered by injected content) would otherwise
		// spawn that many subprocesses/connections (bash, grep, fetch, ...)
		// simultaneously with no ceiling. Shared by the concurrent pool and
		// the sequential gated walker below, so the combined in-flight total
		// still never exceeds it.
		sem := make(chan struct{}, maxConcurrentToolCalls)

		// cancelledResult emits an immediate "cancelled" tool_result for a
		// call that never got to run at all — used when the turn's ctx is
		// already done before a queued call (concurrent-pool or gated) gets
		// its turn. Without this, a call still waiting for a semaphore slot,
		// or still queued behind an earlier gated call, would otherwise
		// either block past cancellation or (for a batched subagent's TUI
		// widget) never resolve at all.
		cancelledResult := func(idx int, call tools.ToolCall) {
			results[idx] = tools.ToolResult{Error: "cancelled"}
			a.sendEvent(OutputEvent{
				Type:       OutputToolResult,
				ToolName:   call.Name,
				ToolCallID: call.ID,
				ToolError:  "cancelled",
			})
		}

		runTool := func(idx int, call tools.ToolCall) {
			// approvalRec is attached to callCtx below, before Execute —
			// nil here covers the memoized-read and pre-Execute-panic
			// emit() calls further down, which never reached an approval
			// gate at all, so they correctly report no marker.
			var approvalRec *tools.ApprovalRecord
			emit := func(res tools.ToolResult) {
				results[idx] = res
				humanApproval := ""
				if approvalRec != nil && approvalRec.Asked {
					if approvalRec.Allowed {
						humanApproval = "approved"
					} else {
						humanApproval = "denied"
					}
				}
				a.sendEvent(OutputEvent{
					Type:              OutputToolResult,
					ToolName:          call.Name,
					ToolCallID:        call.ID,
					ToolResultContent: res.Content,
					ToolError:         res.Error,
					HumanApproval:     humanApproval,
				})
			}
			// A panic anywhere below (inside a.tools.Execute, a memo
			// lookup, etc.) would otherwise kill this goroutine
			// unrecovered — which kills the ENTIRE process (interactive
			// session or subagent child alike), abandoning every other
			// tool call still in flight this round and losing the
			// conversation. Recovering here turns it into an ordinary
			// failed tool_result instead: the model sees a real error
			// for THIS call and the turn, and the agent, continue
			// normally — exactly like any other tool returning an error.
			// The full stack trace goes to the log (a developer concern);
			// the model only needs the short message.
			defer func() {
				if r := recover(); r != nil {
					log.Printf("tool %q panicked: %v\n%s", call.Name, r, debug.Stack())
					emit(tools.TrimToolResult(tools.ToolResult{
						Error: fmt.Sprintf("tool %q panicked: %v", call.Name, r),
					}))
				}
			}()
			// A `read` at the same (or a narrower) range as an earlier,
			// still-unchanged read in this session doesn't need to hit
			// the filesystem again — see read_memo.go.
			if call.Name == "read" {
				if stub, ok := a.tryMemoizedRead(dispatchCwd, call.Input); ok {
					emit(tools.ToolResult{Content: stub})
					return
				}
			}
			callCtx := tools.WithToolCallID(ctx, call.ID)
			callCtx, approvalRec = tools.WithApprovalRecord(callCtx)
			res, err := a.tools.Execute(callCtx, call.Name, call.Input)
			if err != nil {
				res = tools.TrimToolResult(tools.ToolResult{Error: err.Error()})
			}
			switch {
			case call.Name == "read" && res.Error == "":
				a.recordRead(dispatchCwd, call.Input, res.Content)
			case call.Name == "edit" || call.Name == "write":
				a.invalidateReadMemo(dispatchCwd, call.Input)
			}
			emit(res)
		}

		var gatedIdx []int
		for i, tc := range toolCalls {
			if isGatedCall(tc) {
				gatedIdx = append(gatedIdx, i)
				continue
			}
			wg.Add(1)
			go func(idx int, call tools.ToolCall) {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					// Queued behind the cap when the turn was cancelled —
					// resolve now instead of waiting on a slot a finished
					// sibling may never free promptly.
					cancelledResult(idx, call)
					return
				}
				defer func() { <-sem }()
				runTool(idx, call)
			}(i, tc)
		}
		if len(gatedIdx) > 0 {
			wg.Add(1)
			go func(idxs []int) {
				defer wg.Done()
				for _, idx := range idxs {
					call := toolCalls[idx]
					if ctx.Err() != nil {
						// The whole turn is already cancelled — every
						// remaining gated call (e.g. every not-yet-started
						// subagent in this round) must resolve immediately,
						// not sit spinning forever with no tool_result ever
						// emitted for it.
						cancelledResult(idx, call)
						continue
					}
					select {
					case sem <- struct{}{}:
					case <-ctx.Done():
						cancelledResult(idx, call)
						continue
					}
					runTool(idx, call)
					<-sem
				}
			}(gatedIdx)
		}
		wg.Wait()

		// Persist tool_result messages even if the context was cancelled — the
		// results are already computed and leaving orphaned tool_use blocks
		// without results would cause a provider 400 on the next request.
		// dispatchCwd is reused here (rather than a fresh a.cwd() call) since
		// cwd can't change mid-turn.
		turnCwd := dispatchCwd
		sysCtxPaths := a.systemPromptContextPaths(turnCwd)
		for i, result := range results {
			toolBlock := provider.ContentBlock{
				Type:       "tool_result",
				ToolCallID: toolCalls[i].ID,
			}
			if result.Error != "" {
				toolBlock.ToolIsError = true
				toolBlock.ToolResult = result.Error
			} else {
				toolBlock.ToolResult = result.Content
				// Attach any not-yet-loaded AGENTS.md for the file's directory
				// (and the cwd→dir chain) so the model gets its project rules.
				toolBlock.ToolResult += a.contextInjectionForFile(
					turnCwd, toolCalls[i].Name, toolCalls[i].Input, sysCtxPaths)
			}
			// If the user asked us to wrap up (Ctrl+G — subagents only), append the
			// nudge to the last tool result and force the next completion to be
			// tool-less, so the model can't just acknowledge the nudge and keep
			// working — it must produce the final answer right away.
			if i == len(results)-1 && a.expedite.Swap(false) {
				toolBlock.ToolResult += expediteNudge
				a.expediteForceNoTools.Store(true)
			}
			// A message the user queued while this tool round was running is
			// folded into the last tool result here too, rather than appended as
			// its own user-role row: these tool_result rows get coalesced into
			// one "user" wire message (see anthropic.go), and a separate fresh
			// user row right after would make two consecutive user-role
			// messages, which Anthropic rejects. This gets it to the model on
			// its very next completion instead of only once the whole turn
			// (which may run many more tool rounds) finally ends.
			if i == len(results)-1 && a.pendingInputFn != nil {
				if segments, ok := a.pendingInputFn(); ok {
					toolBlock.ToolResult += "\n\n[Queued user message]\n" + flattenSegments(segments)
				}
			}

			// A tool that loaded an image for the model to see (currently
			// only `read` on an image file) carries it here rather than
			// inlined in ToolResult.Content — see ToolResult's doc comment.
			// Append it as a sibling content block in this SAME tool-role
			// message: every provider already knows how to load + encode an
			// "image" block, since that's exactly how a user-attached image
			// reaches them (see anthropic.go/openai.go/ollama.go/xai.go's
			// own "image" cases).
			blocks := []provider.ContentBlock{toolBlock}
			if result.ImagePath != "" {
				blocks = append(blocks, provider.ContentBlock{
					Type:      "image",
					MediaType: result.MediaType,
					ImagePath: result.ImagePath,
					ImageName: result.ImageName,
				})
			}
			toolContent, err := contentBlocksToJSON(blocks)
			if err != nil {
				return a.failTurn(fmt.Sprintf("Marshal error: %v", err), fmt.Errorf("marshal tool result: %w", err))
			}
			if err := a.store.AppendMessage(&store.Message{
				SessionID: a.sessionID,
				Role:      "tool",
				Content:   toolContent,
			}); err != nil {
				return a.failTurn(fmt.Sprintf("Store error: %v", err), fmt.Errorf("append tool result message: %w", err))
			}

			a.sessionToolCalls++
			if result.Error != "" {
				a.sessionToolErrors++
			}

		}

		a.UpdateStatus()

		if err := ctx.Err(); err != nil {
			return a.endTurn(err)
		}

		// Loop: re-stream with updated context (tool results now in store).
	}

	return nil
}

// buildRequest assembles a provider.Request from the store: active messages,
// system prompt, compaction summary (if set), and tool definitions.
func (a *Agent) buildRequest() (*provider.Request, error) {
	// Get session.
	sess, err := a.store.GetSession(a.sessionID)
	if err != nil {
		return nil, fmt.Errorf("get session: %w", err)
	}

	// Get active messages (deleted_at IS NULL AND compacted = 0).
	msgs, err := a.store.GetMessages(a.sessionID)
	if err != nil {
		return nil, fmt.Errorf("get messages: %w", err)
	}

	// Convert store messages to provider messages.
	providerMsgs := make([]provider.Message, 0, len(msgs))
	for _, m := range msgs {
		pm, err := messageToProvider(m)
		if err != nil {
			return nil, fmt.Errorf("convert message %s: %w", m.ID, err)
		}
		providerMsgs = append(providerMsgs, pm)
	}

	// System prompt carries only the always-relevant context: the global
	// ~/.poisson AGENTS.md and the cwd's own. Directory-specific AGENTS.md for
	// files the agent works on are injected into the conversation on demand (see
	// contextInjectionForFile), not the system prompt.
	contextFiles := project.LoadProjectContextFiles(sess.Cwd, config.ConfigDir(), nil)
	toolNames := make([]string, 0)
	if a.tools != nil {
		for _, td := range a.tools.Definitions() {
			toolNames = append(toolNames, td.Name)
		}
	}
	var skillsText string
	if a.skillsEnabled && len(a.skills) > 0 {
		skillsText = skills.FormatSkillsForPrompt(a.skills)
	}
	sysPrompt := project.BuildSystemPrompt(project.BuildSystemPromptOptions{
		Cwd:          sess.Cwd,
		ToolNames:    toolNames,
		ContextFiles: contextFiles,
		SkillsText:   skillsText,
	})

	var systemBlocks []provider.SystemBlock
	systemBlocks = append(systemBlocks, provider.SystemBlock{
		Text: sysPrompt,
	})
	if sess.CompactionSummary != nil && *sess.CompactionSummary != "" {
		systemBlocks = append(systemBlocks, provider.SystemBlock{
			Text: *sess.CompactionSummary,
		})
	}

	// Tool definitions.
	var toolDefs []provider.ToolDef
	if a.tools != nil {
		toolDefs = a.tools.Definitions()
	}

	// Cache the system-side token estimate for the status bar: the system prompt
	// plus each tool's serialized schema. Messages and the compaction summary are
	// counted separately by estimateMessagesTokens, so they are excluded here.
	sysEst := a.EstimateTokens(sysPrompt)
	for _, td := range toolDefs {
		if b, err := json.Marshal(td); err == nil {
			sysEst += a.EstimateTokens(string(b))
		}
	}
	a.sysTokensEstimate.Store(int64(sysEst))

	model := a.Model()
	if model == "" {
		model = sess.Model
	}

	return &provider.Request{
		Model:    model,
		System:   systemBlocks,
		Messages: providerMsgs,
		Tools:    toolDefs,
		Effort:   a.effort,
		CacheKey: a.sessionID, // stable per conversation → OpenAI prompt caching
	}, nil
}

// --- Helpers ----------------------------------------------------------

// contentBlockJSON is the JSON representation of a ContentBlock for store
// persistence. Field names use snake_case to match the store's FTS
// extractor. Its fields are declared in exactly the same names/types/order
// as provider.ContentBlock (tags aside) so contentBlocksToJSON and
// messageToProvider below can convert between the two with a plain Go
// struct conversion instead of a hand-copied field list per direction —
// the compiler itself rejects the conversion if the two types ever drift.
type contentBlockJSON struct {
	Type        string          `json:"type"`
	Text        string          `json:"text,omitempty"`
	ToolCallID  string          `json:"tool_call_id,omitempty"`
	ToolName    string          `json:"tool_name,omitempty"`
	ToolInput   json.RawMessage `json:"tool_input,omitempty"`
	ToolResult  string          `json:"tool_result,omitempty"`
	ToolIsError bool            `json:"tool_is_error,omitempty"`
	FileRef     string          `json:"file_ref,omitempty"`

	MediaType string `json:"media_type,omitempty"`
	ImagePath string `json:"image_path,omitempty"`
	ImageName string `json:"image_name,omitempty"`

	Thinking          string `json:"thinking,omitempty"`
	ThinkingSignature string `json:"thinking_signature,omitempty"`
	Redacted          bool   `json:"redacted,omitempty"`
}

// contentBlocksToJSON serializes a slice of ContentBlocks into a JSON string
// suitable for the store's content column. An empty slice produces "[]".
func contentBlocksToJSON(blocks []provider.ContentBlock) (string, error) {
	if blocks == nil {
		blocks = []provider.ContentBlock{}
	}
	out := make([]contentBlockJSON, len(blocks))
	for i, b := range blocks {
		out[i] = contentBlockJSON(b)
	}
	data, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// messageToProvider converts a store.Message to a provider.Message by parsing
// the content JSON into ContentBlocks. If the content is not valid JSON it
// falls back to a single text block with the raw content.
func messageToProvider(msg store.Message) (provider.Message, error) {
	var blocks []contentBlockJSON
	if err := json.Unmarshal([]byte(msg.Content), &blocks); err != nil {
		return provider.Message{
			Role:    msg.Role,
			Content: []provider.ContentBlock{{Type: "text", Text: msg.Content}},
		}, nil
	}
	content := make([]provider.ContentBlock, len(blocks))
	for i, b := range blocks {
		content[i] = provider.ContentBlock(b)
	}
	return provider.Message{
		Role:    msg.Role,
		Content: content,
	}, nil
}

// buildAssistantBlocks assembles the content blocks for the assistant message
// from the streamed text and collected tool calls.
func buildAssistantBlocks(thinking, thinkingSig string, redacted []provider.ContentBlock, text string, toolCalls []provider.ToolCall) []provider.ContentBlock {
	var blocks []provider.ContentBlock
	// Thinking blocks must precede text and tool_use (Anthropic ordering).
	blocks = append(blocks, redacted...)
	if thinking != "" {
		blocks = append(blocks, provider.ContentBlock{
			Type: "thinking", Thinking: thinking, ThinkingSignature: thinkingSig,
		})
	}
	if text != "" {
		blocks = append(blocks, provider.ContentBlock{Type: "text", Text: text})
	}
	for _, tc := range toolCalls {
		// Backstop: updateToolCall already normalizes empty Input to "{}" at
		// EventToolUseStop (the primary fix), so this should be a no-op by
		// the time we get here — kept because a corrupt/empty tool_input
		// written to the store breaks request serialization on every future
		// turn, and that cost is worth one cheap extra check at the boundary.
		input := tc.Input
		if len(input) == 0 {
			input = json.RawMessage("{}")
		}
		blocks = append(blocks, provider.ContentBlock{
			Type:       "tool_use",
			ToolCallID: tc.ID,
			ToolName:   tc.Name,
			ToolInput:  input,
		})
	}
	return blocks
}

// repairDanglingToolUse guards against a session resumed after the process
// died mid tool-round (killed, crashed, machine lost power) rather than via
// graceful cancellation. persistPartialTurnOnCancel handles the graceful
// path; this handles the case where nothing ran at all before the process
// exited, so the assistant's tool_use message was persisted (buildAssistantBlocks
// writes it before tools are dispatched) but some or all of the matching
// tool_result messages never made it to the store. Anthropic (and others)
// reject any request whose history has a tool_use without a following
// tool_result, so an unrepaired history is permanently stuck erroring on
// every resume. Must only be called before a new turn starts (see call site
// in PromptSegmentsWithContext) — that's the only point where a dangling
// trailing tool_use is unambiguously dead rather than a tool call still
// legitimately running in another goroutine of this same process.
//
// A dangling tool_use always sits directly under a run of "tool" messages
// (whatever of its own results made it to the store — possibly none) and,
// if the same broken history was retried one or more times before this fix
// existed (each retry's request 400s, but appendUserMessage had already run,
// so the row sticks), under a further run of unanswered "user" retries on
// top of that. repairDanglingToolUse walks back through both runs to find
// the assistant message underneath and only acts if that assistant message
// really has unresolved tool_use — a legitimately pre-seeded trailing user
// message unrelated to this bug (e.g. history assembled directly for a test,
// or some future flow that intentionally idles on one) must be left alone,
// since a lone trailing user row on its own isn't proof of anything broken.
//
// When it does find a genuine dangling tool_use, it:
//  1. Appends a synthetic error tool_result for every tool_use ID the
//     assistant message emitted that has no matching tool_result among the
//     trailing "tool" messages — an honest record that the tool's outcome
//     was lost, not a fabricated result.
//  2. Soft-deletes the trailing "user" retries sitting on top of it: they're
//     failed re-attempts against this exact same broken history, add nothing
//     (the freshest is about to be superseded by the new prompt this call is
//     starting anyway), and left in place are themselves illegal — Anthropic
//     rejects consecutive same-role messages just as it rejects a dangling
//     tool_use. Soft-deleted, not hard-deleted, so they stay inspectable.
//
// Must only be called before a new turn starts (see call site in
// PromptSegmentsWithContext) — that's the only point where a dangling
// trailing tool_use is unambiguously dead rather than a tool call still
// legitimately running in another goroutine of this same process. Idempotent:
// once repaired, later calls find nothing unresolved and are a no-op.
func (a *Agent) repairDanglingToolUse() error {
	msgs, err := a.store.GetMessages(a.sessionID)
	if err != nil {
		return fmt.Errorf("get messages: %w", err)
	}

	end := len(msgs)
	i := end
	for i > 0 && msgs[i-1].Role == "user" {
		i--
	}
	staleRetries := msgs[i:end]
	toolEnd := i
	for i > 0 && msgs[i-1].Role == "tool" {
		i--
	}
	if i == 0 || msgs[i-1].Role != "assistant" {
		return nil
	}
	var assistantBlocks []contentBlockJSON
	if err := json.Unmarshal([]byte(msgs[i-1].Content), &assistantBlocks); err != nil {
		return nil
	}
	resolved := map[string]bool{}
	for _, m := range msgs[i:toolEnd] {
		var toolBlocks []contentBlockJSON
		if json.Unmarshal([]byte(m.Content), &toolBlocks) != nil {
			continue
		}
		for _, b := range toolBlocks {
			if b.Type == "tool_result" {
				resolved[b.ToolCallID] = true
			}
		}
	}
	var missing []string
	for _, b := range assistantBlocks {
		if b.Type == "tool_use" && !resolved[b.ToolCallID] {
			missing = append(missing, b.ToolCallID)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	for _, stale := range staleRetries {
		if err := a.store.SoftDeleteMessage(stale.ID); err != nil {
			return fmt.Errorf("prune stale retry message: %w", err)
		}
	}
	for _, id := range missing {
		content, err := contentBlocksToJSON([]provider.ContentBlock{{
			Type: "tool_result", ToolCallID: id, ToolIsError: true,
			ToolResult: "interrupted — the session ended before this tool's result was recorded",
		}})
		if err != nil {
			return fmt.Errorf("marshal recovered tool result: %w", err)
		}
		if err := a.store.AppendMessage(&store.Message{SessionID: a.sessionID, Role: "tool", Content: content}); err != nil {
			return fmt.Errorf("append recovered tool result: %w", err)
		}
	}
	return nil
}

// updateToolCall updates a tool call in the list by matching ID. If the ID
// is empty or no match is found, the last entry is updated as a fallback.
// persistPartialTurnOnCancel saves whatever the model had already produced
// before the user cancelled mid-stream (text, thinking, and any *complete*
// tool_use blocks) as a real assistant message, instead of silently
// discarding it. Without this, the partial response stayed visible in the
// live scrollback (it already streamed as OutputText/OutputThinking events)
// but vanished from what actually gets sent on the next request — what's
// shown and what's in history must match.
//
// A tool_use whose input JSON never finished streaming (cancelled mid
// argument, possible on providers that emit incremental tool-input deltas)
// is dropped rather than persisted: writing incomplete/invalid JSON into a
// tool_input column would break request serialization on every future turn.
// Any tool_use that DID finish (valid, complete arguments) gets a synthetic
// "cancelled by user" tool_result appended right after it — every tool_use
// needs a matching tool_result on the next request or the provider rejects
// the whole thing with a 400; the tool never actually ran, so this is an
// honest record of that, not a fabricated result.
func (a *Agent) persistPartialTurnOnCancel(text, thinkingText, thinkingSig string, redacted []provider.ContentBlock, toolCalls []provider.ToolCall) {
	var complete []provider.ToolCall
	for _, tc := range toolCalls {
		if len(tc.Input) > 0 && json.Valid(tc.Input) {
			complete = append(complete, tc)
		}
	}
	assistantBlocks := buildAssistantBlocks(thinkingText, thinkingSig, redacted, text, complete)
	if len(assistantBlocks) == 0 {
		return
	}
	content, err := contentBlocksToJSON(assistantBlocks)
	if err != nil {
		log.Printf("warning: marshal cancelled turn: %v", err)
		return
	}
	if err := a.store.AppendMessage(&store.Message{SessionID: a.sessionID, Role: "assistant", Content: content}); err != nil {
		log.Printf("warning: append cancelled assistant message: %v", err)
		return
	}
	for _, tc := range complete {
		block := provider.ContentBlock{
			Type: "tool_result", ToolCallID: tc.ID,
			ToolIsError: true, ToolResult: "cancelled by user before this tool ran",
		}
		toolContent, err := contentBlocksToJSON([]provider.ContentBlock{block})
		if err != nil {
			log.Printf("warning: marshal cancelled tool result: %v", err)
			continue
		}
		if err := a.store.AppendMessage(&store.Message{SessionID: a.sessionID, Role: "tool", Content: toolContent}); err != nil {
			log.Printf("warning: append cancelled tool result: %v", err)
		}
	}
}

// updateToolCall updates a tool call in the list by matching ID. If the ID
// is empty or no match is found, the last entry is updated as a fallback.
// final marks the EventToolUseStop update — provider.go documents ToolCall
// there as "final Input", but a tool with an all-optional schema can
// legitimately finish with zero argument bytes (some providers never stream
// an arguments delta at all for it). Every tool's Execute does
// json.Unmarshal(input, &in) first, and Unmarshal rejects a zero-length
// RawMessage outright ("unexpected end of JSON input") — so a valid no-args
// call would fail live even though it's semantically identical to "{}".
// Normalizing only on the final update (not every delta) leaves a call that
// never reaches EventToolUseStop — genuinely cancelled mid-argument —
// untouched, so persistPartialTurnOnCancel's len>0/json.Valid check below
// still correctly drops it as incomplete rather than complete-but-empty.
func (a *Agent) updateToolCall(toolCalls []provider.ToolCall, updated *provider.ToolCall, final bool) {
	if updated == nil || len(toolCalls) == 0 {
		return
	}
	// A later event replaces the whole struct, so the name has to be
	// canonicalized here as well or the Start event's mapping is undone.
	updated.Name = a.canonicalToolName(updated.Name)
	if final && len(updated.Input) == 0 {
		updated.Input = json.RawMessage("{}")
	}
	if updated.ID != "" {
		for i := range toolCalls {
			if toolCalls[i].ID == updated.ID {
				toolCalls[i] = *updated
				return
			}
		}
	}
	// Fallback: update the last entry.
	toolCalls[len(toolCalls)-1] = *updated
}

// canonicalToolName maps a provider-emitted tool name onto the registered
// tool it means. The Anthropic stealth path advertises tools under Claude
// Code's MCP naming convention (bash -> mcp_Bash) and normally unwraps the
// names it gets back, but a model can echo a wire-shaped name anywhere — a
// non-stealth request, another provider, or nested inside batch's arguments.
// Resolving centrally here means one bad spelling no longer costs a whole
// round trip to a "tool not registered" error. Unknown names pass through so
// the error still reports exactly what the model asked for.
func (a *Agent) canonicalToolName(name string) string {
	if a == nil || a.tools == nil {
		return name
	}
	return a.tools.Canonical(name)
}

func (a *Agent) computeCost(providerID, model string, input, output, cacheRead, cacheWrite int) float64 {
	return pricing.ComputeCost(a.config, providerID, model, input, output, cacheRead, cacheWrite)
}

// recordAPICall records a row in the api_calls table with exact usage and
// computed cost, and returns the generated ID.
func (a *Agent) recordAPICall(usage *provider.Usage) (string, error) {
	return a.recordAPICallFor(usage, "main", a.providerID(), a.currentModel())
}

func (a *Agent) recordCompactionAPICall(providerID, model string, usage *provider.Usage) error {
	_, err := a.recordAPICallFor(usage, "compaction", providerID, model)
	return err
}

func (a *Agent) recordAuxiliaryAPICall(purpose string, usage *provider.Usage) error {
	_, err := a.recordAPICallFor(usage, purpose, a.providerID(), a.currentModel())
	return err
}

func (a *Agent) recordAPICallFor(usage *provider.Usage, purpose, providerID, model string) (string, error) {
	cost := a.computeCost(providerID, model,
		usage.InputTokens, usage.OutputTokens, usage.CacheReadTokens, usage.CacheWriteTokens)
	return a.recordAPICallCost(usage, purpose, providerID, model, cost)
}

// recordAPICallCost is recordAPICallFor with the price already decided —
// needed when the provider itself reports the exact dollar figure (see
// RecordWebToolCall) rather than leaving it to the local rate table.
func (a *Agent) recordAPICallCost(usage *provider.Usage, purpose, providerID, model string, cost float64) (string, error) {
	cacheRead, cacheWrite := usage.CacheReadTokens, usage.CacheWriteTokens

	// Serialized: seq is max(seq)+1 read from the DB, and tools run in
	// parallel (batch, and web tools inside it), so two concurrent recorders
	// would otherwise pick the same number.
	a.apiCallMu.Lock()
	defer a.apiCallMu.Unlock()

	seq := a.nextAPICallSeq()

	call := &store.APICall{
		SessionID:          a.sessionID,
		Seq:                seq,
		Provider:           providerID,
		Model:              model,
		InputTokens:        usage.InputTokens,
		InputTokensUnknown: usage.InputTokensUnknown,
		OutputTokens:       usage.OutputTokens,
		CacheReadTokens:    cacheRead,
		CacheWriteTokens:   cacheWrite,
		Cost:               cost,
		Purpose:            purpose,
		IsCompaction:       purpose == "compaction",
	}
	if err := a.store.RecordAPICall(call); err != nil {
		return "", err
	}
	a.addCumulativeUsage(usage, cost)
	return call.ID, nil
}

// addCumulativeUsage folds usage and its cost into the running totals (see
// cumUsage). cost is accumulated separately from the tokens because it was
// priced against THIS call's model, which is not necessarily the session's
// current one — the bash-risk classifier can run on a different model.
func (a *Agent) addCumulativeUsage(usage *provider.Usage, cost float64) {
	a.cumUsageMu.Lock()
	defer a.cumUsageMu.Unlock()
	a.cumUsage.InputTokens += usage.InputTokens
	a.cumUsage.OutputTokens += usage.OutputTokens
	a.cumUsage.CacheReadTokens += usage.CacheReadTokens
	a.cumUsage.CacheWriteTokens += usage.CacheWriteTokens
	a.cumUsage.InputTokensUnknown = a.cumUsage.InputTokensUnknown || usage.InputTokensUnknown
	a.cumCost += cost
}

// CumulativeUsage returns the running total of every api_calls row recorded
// for this Agent's session so far. Cheap (in-memory, mutex-guarded) — safe to
// call on every turn-loop tick, unlike GetSessionTokenBreakdown which re-sums
// via SQL. Used by subagent/child mode to relay live spend to the parent.
// Tokens only — see CumulativeCost for what those tokens actually cost.
func (a *Agent) CumulativeUsage() provider.Usage {
	a.cumUsageMu.Lock()
	defer a.cumUsageMu.Unlock()
	return a.cumUsage
}

// CumulativeCost returns the total dollar cost of every api_calls row this
// Agent has recorded for its session, each priced against the model that
// actually served it. Child mode relays this to the parent so a subagent's
// spend is recorded at the price the child really paid, rather than the parent
// re-pricing the whole token blob at the child's main model — see
// subagent.ChildEvent.Cost.
func (a *Agent) CumulativeCost() float64 {
	a.cumUsageMu.Lock()
	defer a.cumUsageMu.Unlock()
	return a.cumCost
}

// RecordSubagentUsage records a finished (or partially finished — e.g. the
// parent turn was cancelled mid-run) subagent invocation's accumulated token
// usage as a "subagent"-purpose api_calls row on the PARENT session, so
// subagent spend counts toward GetSessionCost/GetSessionTokenBreakdown
// instead of vanishing with the child's ephemeral, throwaway DB. Deliberately
// does not go through recordAuxiliaryAPICall: that helper prices against
// a.providerID()/a.currentModel() — this Agent's *current* provider/model —
// which could have moved on (via /model, /provider) by the time a
// long-running subagent finishes. providerID/model here must be whatever the
// subagent was actually spawned against (SubagentTool captures them once, at
// spawn time). Returns the recorded cost.
//
// childCost, when positive, is the child's own cumulative cost (see
// subagent.ChildEvent.Cost) and is recorded verbatim. The child prices each of
// its calls against the model that actually served it, so this is the only
// figure that gets a session mixing models right — a bash-risk classifier
// running under a /classifier-model pin spends at a different rate than the
// child's main model, and re-pricing the whole token blob at one model here
// would be wrong for that slice. Falls back to pricing the blob at
// providerID/model when the child reported no cost (nothing recorded yet).
func (a *Agent) RecordSubagentUsage(providerID, model string, usage *provider.Usage, childCost float64) (float64, error) {
	cost := childCost
	if cost <= 0 {
		cost = a.computeCost(providerID, model,
			usage.InputTokens, usage.OutputTokens, usage.CacheReadTokens, usage.CacheWriteTokens)
	}

	// Same seq race recordAPICallCost guards against: a batch can finish
	// several subagents at once, each racing this function's own
	// nextAPICallSeq() read against every other recorder in the session.
	a.apiCallMu.Lock()
	defer a.apiCallMu.Unlock()

	call := &store.APICall{
		SessionID:          a.sessionID,
		Seq:                a.nextAPICallSeq(),
		Provider:           providerID,
		Model:              model,
		InputTokens:        usage.InputTokens,
		InputTokensUnknown: usage.InputTokensUnknown,
		OutputTokens:       usage.OutputTokens,
		CacheReadTokens:    usage.CacheReadTokens,
		CacheWriteTokens:   usage.CacheWriteTokens,
		Cost:               cost,
		Purpose:            "subagent",
	}
	if err := a.store.RecordAPICall(call); err != nil {
		return 0, err
	}
	return cost, nil
}

// RecordWebToolCall banks an API call a web tool's backend made outside
// provider.Stream — the Anthropic search/summarize helper model, or web_ask's
// Grok call (see tools.WebCall). Without this those tokens never reach
// api_calls, and /cost, `px cost` and a subagent's reported spend all silently
// undercount every session that used a web tool.
//
// The row is priced against the backend's own provider and model, never the
// session's: web_ask spends xAI credits while the session runs on Anthropic,
// and the Anthropic backends spend on a small helper model, not the session
// model. As everywhere else in poisson, an OAuth/subscription session gets
// shadow pricing at published API rates — the tokens are real, the dollars are
// indicative.
func (a *Agent) RecordWebToolCall(c tools.WebCall) {
	cost := c.Cost
	if cost <= 0 {
		// No provider-reported figure: price the tokens locally, plus the
		// per-search fee Anthropic bills on top of them.
		cost = a.computeCost(c.Provider, c.Model,
			c.Usage.InputTokens, c.Usage.OutputTokens, c.Usage.CacheReadTokens, c.Usage.CacheWriteTokens) +
			pricing.SearchCost(a.config, c.Provider, c.Model, c.SearchRequests)
	}
	usage := c.Usage
	if _, err := a.recordAPICallCost(&usage, c.Purpose, c.Provider, c.Model, cost); err != nil {
		log.Printf("record %s api call: %v", c.Purpose, err)
	}
}

// nextAPICallSeq returns the next sequence number for api_calls in this
// session (max(seq) + 1, or 1 if no rows yet). Callers hold apiCallMu.
func (a *Agent) nextAPICallSeq() int {
	var ms int
	row := a.store.DB().QueryRow(
		`SELECT COALESCE(MAX(seq), 0) FROM api_calls WHERE session_id = ?`,
		a.sessionID)
	if err := row.Scan(&ms); err != nil {
		return 1
	}
	return ms + 1
}

// currentModel returns the model from the session, falling back to config
// (via the same config.Providers registry defaultModel uses above).
func (a *Agent) currentModel() string {
	sess, err := a.store.GetSession(a.sessionID)
	if err != nil || sess == nil {
		return provider.DefaultModel(a.providerID(), a.config)
	}
	return sess.Model
}

// sendEvent sends an OutputEvent to the output channel. If the channel is nil
// (no TUI attached), the event is silently dropped.
func (a *Agent) sendEvent(ev OutputEvent) {
	if a.outputChan != nil {
		a.outputChan <- ev
	}
}

// endTurn sends OutputDone (every turn-ending exit path must send exactly
// one) and returns err unchanged. Used for the silent exits — ctx
// cancellation, user Ctrl+C — that aren't user-facing failures worth an
// OutputError.
func (a *Agent) endTurn(err error) error {
	a.sendEvent(OutputEvent{Type: OutputDone})
	return err
}

// failTurn reports a user-facing turn failure: an OutputError with text,
// then OutputDone, then returns err (normally fmt.Errorf-wrapped by the
// caller) so every fatal abort in PromptSegmentsWithContext/runTurn reports
// itself the same way to the TUI/CLI.
func (a *Agent) failTurn(text string, err error) error {
	a.sendEvent(OutputEvent{Type: OutputError, Text: text})
	a.sendEvent(OutputEvent{Type: OutputDone})
	return err
}
