package tools

import (
	"context"

	"poisson/internal/store"
)

// ApprovalFn is called before executing dangerous bash commands. ctx is the
// tool's turn context so a cancelled turn also cancels the risk classification.
type ApprovalFn func(ctx context.Context, command, description, workdir string) bool

// BuildOptions configures which tools to register.
type BuildOptions struct {
	Cwd         string
	Store       *store.Store
	Sandbox     bool
	ApprovalFn  ApprovalFn
	SubApproval SubagentApproval
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
		approval = func(context.Context, string, string, string) bool { return false }
	}

	reg.Register(NewBashTool(opts.Cwd, opts.Sandbox, approval))
	reg.Register(NewReadTool(opts.Cwd))
	reg.Register(NewWriteTool(opts.Cwd))
	reg.Register(NewEditTool(opts.Cwd))
	reg.Register(NewSearchTool(opts.Cwd))
	reg.Register(NewLsTool(opts.Cwd))
	reg.Register(NewGlobTool(opts.Cwd))
	reg.Register(NewExaSearchTool())
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

// BindSubagentProgress wires a live turn-count progress callback (for the
// running subagent card) onto the subagent tool.
func BindSubagentProgress(reg *Registry, fn func(toolCallID string, turns int)) {
	t, ok := reg.Get("subagent")
	if !ok {
		return
	}
	if st, ok := t.(*SubagentTool); ok {
		st.SetProgressFn(fn)
	}
}
