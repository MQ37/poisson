package tools

import (
	"context"

	"github.com/mq37/poisson/internal/auth"
	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/sandbox"
	"github.com/mq37/poisson/internal/store"
)

// ApprovalFn is called before executing a dangerous bash command or touching
// a sensitive file path (read/write/edit). ctx is the tool's turn context so
// a cancelled turn also cancels the risk classification. reason is an
// optional human-supplied explanation when denied (empty when allowed, or
// when the human left it blank) — surfaced to the model so it understands
// *why* a command was rejected, not just that it was.
type ApprovalFn func(ctx context.Context, command, description, workdir string) (allowed bool, reason string)

// BuildOptions configures which tools to register.
type BuildOptions struct {
	Cwd   string
	Store *store.Store
	// Auth is shared by reference with the active chat provider (loaded once
	// in main.go) so WebAskTool's grok backend and XAIProvider refresh/save
	// the same "xai" entry instead of racing two independent copies of
	// ~/.poisson/auth.json. May be nil (web_ask then always uses exa).
	Auth       auth.AuthStore
	ApprovalFn ApprovalFn
	// FileApprovalFn gates read/write/edit against sensitive paths (.env*,
	// SSH/cloud credentials, ~/.poisson secrets, ...). Unlike ApprovalFn it is
	// asked directly — no LLM risk classification — since "does this path
	// match a secrets pattern" is already deterministic. Defaults to
	// deny-all, matching ApprovalFn's fail-closed default.
	FileApprovalFn ApprovalFn
	SubApproval    SubagentApproval
	// Child omits the subagent tool so a subagent cannot spawn further
	// subagents (recursion is bounded to one level). Every other tool remains
	// available to children.
	Child bool
	// SandboxManager, when non-nil, is wired onto the bash tool so a
	// sandboxId-carrying call routes through it instead of erroring, and
	// registers create_sandbox/sandbox_cp/sandbox_destroy. Nil by default (no
	// sandbox support) — a subagent registry gets one only when its parent
	// explicitly authorized specific sandboxIds for it (see
	// docs/sandbox-plan.md's subagent allow-list, not yet implemented).
	SandboxManager *sandbox.Manager
	// SandboxApprovalFn gates create_sandbox requests that ask for mounts or
	// env beyond the base workspace — a plain create_sandbox call with
	// neither never asks. Distinct from ApprovalFn (LLM risk classification)
	// and FileApprovalFn (sensitive-path pattern match): this is "does the
	// request itself carry extra host access," decided before any of those
	// even apply. Defaults to deny-all, matching the other two.
	SandboxApprovalFn ApprovalFn
}

// BuildRegistry constructs the tool registry. A child (subagent) receives every
// tool except subagent; the parent additionally gets subagent when a
// SubApproval handler is supplied.
func BuildRegistry(opts BuildOptions) *Registry {
	sweepStaleSpillFiles()
	reg := NewRegistry()

	approval := opts.ApprovalFn
	if approval == nil {
		approval = func(context.Context, string, string, string) (bool, string) { return false, "" }
	}
	fileApproval := opts.FileApprovalFn
	if fileApproval == nil {
		fileApproval = func(context.Context, string, string, string) (bool, string) { return false, "" }
	}
	sandboxApproval := opts.SandboxApprovalFn
	if sandboxApproval == nil {
		sandboxApproval = func(context.Context, string, string, string) (bool, string) { return false, "" }
	}

	bashTool := NewBashTool(opts.Cwd, approval)
	if opts.SandboxManager != nil {
		bashTool.SetSandboxManager(opts.SandboxManager)
	}
	reg.Register(bashTool)
	reg.Register(NewReadTool(opts.Cwd, fileApproval))
	reg.Register(NewWriteTool(opts.Cwd, fileApproval))
	reg.Register(NewEditTool(opts.Cwd, fileApproval))
	reg.Register(NewGrepTool(opts.Cwd, fileApproval))
	reg.Register(NewGlobTool(opts.Cwd, fileApproval))
	if opts.SandboxManager != nil {
		// create_sandbox is parent-only, same reasoning as the subagent tool
		// below: a subagent may only use sandboxes its parent explicitly
		// authorized (docs/sandbox-plan.md), never mint its own. sandbox_cp/
		// sandbox_destroy/list_sandboxes stay available to a child for now —
		// whether a subagent should be able to destroy a sandbox it didn't
		// create is a real open question, deferred to the subagent allow-
		// list step (docs/sandbox-plan.md "Subagents"), not decided here.
		// list_sandboxes is read-only regardless (browsing grants no access
		// on its own — see docs/sandbox-plan.md's "Crash recovery" section).
		if !opts.Child {
			reg.Register(NewCreateSandboxTool(opts.Cwd, opts.SandboxManager, sandboxApproval))
		}
		reg.Register(NewSandboxCpTool(opts.Cwd, opts.SandboxManager, fileApproval))
		reg.Register(NewSandboxDestroyTool(opts.SandboxManager))
		reg.Register(NewSandboxResurrectTool(opts.SandboxManager))
		reg.Register(NewListSandboxesTool(opts.SandboxManager))
	}
	// web_search and fetch are re-registered with their provider-gated
	// backends by agent.ReloadConfigDependentTools, which runs right after the
	// agent knows its provider; here they get the always-available ones.
	reg.Register(NewWebSearchTool(nil))
	reg.Register(NewWebAskTool(opts.Auth))
	if opts.Store != nil {
		reg.Register(NewRecallTool(opts.Store))
		reg.Register(NewListSessionsTool(opts.Store))
		reg.Register(NewReadMessagesTool(opts.Store))
		reg.Register(NewSetTitleTool(opts.Store))
	}
	// Parent-only: a subagent must never receive the subagent tool, or it could
	// spawn subagents without bound.
	if !opts.Child && opts.SubApproval != nil {
		subagentTool := NewSubagentTool(opts.Cwd, opts.SubApproval)
		if opts.SandboxManager != nil {
			// Lets this session's own subagent calls authorize specific
			// sandboxIds for a spawned child (see docs/sandbox-plan.md's
			// subagent allow-list) — validated against this same Manager,
			// never a foreign one.
			subagentTool.SetSandboxManager(opts.SandboxManager)
		}
		reg.Register(subagentTool)
	}
	// batch last so it can dispatch into every tool already registered.
	// Denied inside batch: batch itself (no recursion) — bash and subagent
	// are both allowed; subagent runs fully concurrently with sibling
	// subagent calls, bash stays serial against other gated tools (see
	// batch.go's mutatingTools).
	reg.Register(NewBatchTool(reg))
	return reg
}

// withSubagentTool looks up the registry's subagent tool and applies fn to
// it, a no-op if the tool isn't registered (e.g. a child registry, which
// never gets one — see BuildRegistry). Shared by every BindSubagent* below
// so adding a new one doesn't mean copy-pasting the lookup again.
func withSubagentTool(reg *Registry, fn func(*SubagentTool)) {
	if t, ok := reg.Get("subagent"); ok {
		if st, ok := t.(*SubagentTool); ok {
			fn(st)
		}
	}
}

// BindSubagentRuntime wires live provider/model/effort resolvers on the subagent tool.
func BindSubagentRuntime(reg *Registry, providerFn, modelFn, effortFn func() string) {
	withSubagentTool(reg, func(st *SubagentTool) { st.SetRuntime(providerFn, modelFn, effortFn) })
}

// BindSessionTitle wires the live current-session-id getter and
// ensure-session-row callback onto set_title, once the owning Agent exists —
// BuildRegistry runs before that, so set_title has neither at construction.
// A no-op when set_title wasn't registered (opts.Store was nil).
func BindSessionTitle(reg *Registry, sessionIDFn func() string, ensureFn func() error) {
	if t, ok := reg.Get("set_title"); ok {
		if st, ok := t.(*SetTitleTool); ok {
			st.SetSessionFns(sessionIDFn, ensureFn)
		}
	}
}

// BindSubagentProgress wires a live turn-count + context-usage progress
// callback (for the running subagent card) onto the subagent tool.
func BindSubagentProgress(reg *Registry, fn func(toolCallID string, turns, contextTokens, contextWindow int, tokensPerSec float64, status string)) {
	withSubagentTool(reg, func(st *SubagentTool) { st.SetProgressFn(fn) })
}

// BindSubagentSkills wires a live "are skills enabled" resolver onto the
// subagent tool, so --no-skills (or /reload-time skill disabling) propagates
// to every spawned subagent instead of children always getting skills
// regardless of the parent's setting.
func BindSubagentSkills(reg *Registry, fn func() bool) {
	withSubagentTool(reg, func(st *SubagentTool) { st.SetSkillsEnabledFn(fn) })
}

// BindSubagentClassifier wires the parent's bash-risk classifier model
// resolver onto the subagent tool, so a /classifier-model pin applies to the
// whole px instance rather than stopping at the child process boundary.
func BindSubagentClassifier(reg *Registry, fn func() string) {
	withSubagentTool(reg, func(st *SubagentTool) { st.SetClassifierModelFn(fn) })
}

// withBatchTool looks up the batch tool on reg and, if present, applies fn —
// same pattern as withSubagentTool.
func withBatchTool(reg *Registry, fn func(*BatchTool)) {
	if t, ok := reg.Get("batch"); ok {
		if bt, ok := t.(*BatchTool); ok {
			fn(bt)
		}
	}
}

// BindBatchSubagentDone wires the callback invoked when a subagent call
// nested inside a batch finishes, so its pre-rendered widget (see agent.go's
// batch tool-start dispatch) can flip to done/error as soon as that call
// itself finishes, instead of only when the whole batch does.
func BindBatchSubagentDone(reg *Registry, fn func(toolCallID string, res ToolResult)) {
	withBatchTool(reg, func(bt *BatchTool) { bt.SetSubagentDoneFn(fn) })
}

// BindSubagentUsage wires the callback that rolls a finished subagent's token
// usage into the parent session's cost (see SubagentTool.usageFn).
func BindSubagentUsage(reg *Registry, fn func(providerID, model string, usage *provider.Usage, childCost float64) (float64, error)) {
	withSubagentTool(reg, func(st *SubagentTool) { st.SetUsageFn(fn) })
}

// BindWebUsage wires the cost sink on every web tool with a backend that
// spends an account's tokens: fetch and web_search (Anthropic's helper model)
// and web_ask (Grok). Called from agent.ReloadConfigDependentTools, which runs
// on every entry point (REPL, headless, subagent child, cost-eval) and after
// each provider switch — the sink has to be re-applied there anyway, since
// that is where fetch and web_search get re-registered.
func BindWebUsage(reg *Registry, fn WebUsageFn) {
	for _, name := range []string{"fetch", "web_search", "web_ask"} {
		t, ok := reg.Get(name)
		if !ok {
			continue
		}
		if sink, ok := t.(interface{ SetUsageFn(WebUsageFn) }); ok {
			sink.SetUsageFn(fn)
		}
	}
}
