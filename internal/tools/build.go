package tools

import (
	"context"

	"poisson/internal/auth"
	"poisson/internal/store"
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
	reg.Register(NewSearchTool(opts.Cwd))
	reg.Register(NewLsTool(opts.Cwd))
	reg.Register(NewGlobTool(opts.Cwd))
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
	return reg
}

// BindSubagentRuntime wires live provider/model/effort resolvers on the subagent tool.
func BindSubagentRuntime(reg *Registry, providerFn, modelFn, effortFn func() string) {
	t, ok := reg.Get("subagent")
	if !ok {
		return
	}
	if st, ok := t.(*SubagentTool); ok {
		st.SetRuntime(providerFn, modelFn, effortFn)
	}
}

// BindSubagentProgress wires a live turn-count + context-usage progress
// callback (for the running subagent card) onto the subagent tool.
func BindSubagentProgress(reg *Registry, fn func(toolCallID string, turns, contextTokens, contextWindow int, status string)) {
	t, ok := reg.Get("subagent")
	if !ok {
		return
	}
	if st, ok := t.(*SubagentTool); ok {
		st.SetProgressFn(fn)
	}
}

// BindSubagentSkills wires a live "are skills enabled" resolver onto the
// subagent tool, so --no-skills (or /reload-time skill disabling) propagates
// to every spawned subagent instead of children always getting skills
// regardless of the parent's setting.
func BindSubagentSkills(reg *Registry, fn func() bool) {
	t, ok := reg.Get("subagent")
	if !ok {
		return
	}
	if st, ok := t.(*SubagentTool); ok {
		st.SetSkillsEnabledFn(fn)
	}
}
