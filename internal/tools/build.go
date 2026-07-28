package tools

import (
	"context"

	"github.com/mq37/poisson/internal/auth"
	"github.com/mq37/poisson/internal/provider"
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
	Sandbox    bool
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

	reg.Register(NewBashTool(opts.Cwd, opts.Sandbox, approval))
	reg.Register(NewReadTool(opts.Cwd, opts.Sandbox, fileApproval))
	reg.Register(NewWriteTool(opts.Cwd, opts.Sandbox, fileApproval))
	reg.Register(NewEditTool(opts.Cwd, opts.Sandbox, fileApproval))
	reg.Register(NewGrepTool(opts.Cwd, opts.Sandbox, fileApproval))
	reg.Register(NewGlobTool(opts.Cwd, opts.Sandbox, fileApproval))
	reg.Register(NewWebSearchTool())
	reg.Register(NewWebAskTool(opts.Auth))
	if opts.Store != nil {
		reg.Register(NewRecallTool(opts.Store))
	}
	// Parent-only: a subagent must never receive the subagent tool, or it could
	// spawn subagents without bound.
	if !opts.Child && opts.SubApproval != nil {
		reg.Register(NewSubagentTool(opts.Cwd, opts.SubApproval))
	}
	// batch last so it can dispatch into every tool already registered.
	// Denied inside batch: batch itself (no recursion) — bash and subagent
	// are allowed but subagent runs serial (see batch.go).
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
