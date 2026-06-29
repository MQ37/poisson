package tools

import (
	"strings"

	"poisson/internal/store"
)

// ApprovalFn is called before executing dangerous bash commands.
type ApprovalFn func(command, description, workdir string) bool

// BuildOptions configures which tools to register.
type BuildOptions struct {
	Cwd         string
	Store       *store.Store
	Sandbox     bool
	ApprovalFn  ApprovalFn
	SubOutput      SubagentOutput
	SubApproval    SubagentApproval
	SubProvider    string
	SubModel       string
	// Tools is a comma-separated allowlist for child mode. Empty registers the
	// full parent tool set.
	Tools string
}

type toolEntry struct {
	name    string
	factory func(BuildOptions) Tool
}

// childCatalog maps tool names allowed in subagent child mode.
var childCatalog = map[string]func(BuildOptions) Tool{
	"read": func(o BuildOptions) Tool { return NewReadTool(o.Cwd) },
	"write": func(o BuildOptions) Tool {
		return NewWriteTool(o.Cwd)
	},
	"edit": func(o BuildOptions) Tool { return NewEditTool(o.Cwd) },
	"bash": func(o BuildOptions) Tool {
		approval := o.ApprovalFn
		if approval == nil {
			approval = func(string, string, string) bool { return false }
		}
		return NewBashTool(o.Cwd, o.Sandbox, approval)
	},
	"search": func(o BuildOptions) Tool { return NewSearchTool(o.Cwd) },
	"ls":     func(o BuildOptions) Tool { return NewLsTool(o.Cwd) },
	"glob":   func(o BuildOptions) Tool { return NewGlobTool(o.Cwd) },
}

// BuildRegistry constructs a tool registry for parent or child mode.
func BuildRegistry(opts BuildOptions) *Registry {
	reg := NewRegistry()
	if opts.Tools != "" {
		for _, name := range strings.Split(opts.Tools, ",") {
			name = strings.TrimSpace(name)
			if factory, ok := childCatalog[name]; ok {
				reg.Register(factory(opts))
			}
		}
		return reg
	}

	approval := opts.ApprovalFn
	if approval == nil {
		approval = func(string, string, string) bool { return false }
	}

	parentTools := []toolEntry{
		{"bash", func(o BuildOptions) Tool { return NewBashTool(o.Cwd, false, approval) }},
		{"read", func(o BuildOptions) Tool { return NewReadTool(o.Cwd) }},
		{"write", func(o BuildOptions) Tool { return NewWriteTool(o.Cwd) }},
		{"edit", func(o BuildOptions) Tool { return NewEditTool(o.Cwd) }},
		{"search", func(o BuildOptions) Tool { return NewSearchTool(o.Cwd) }},
		{"ls", func(o BuildOptions) Tool { return NewLsTool(o.Cwd) }},
		{"glob", func(o BuildOptions) Tool { return NewGlobTool(o.Cwd) }},
	}
	for _, e := range parentTools {
		reg.Register(e.factory(opts))
	}
	if opts.Store != nil {
		reg.Register(NewRecallTool(opts.Store))
	}
	if opts.Store != nil && opts.SubOutput != nil && opts.SubApproval != nil {
		reg.Register(NewSubagentTool(opts.Cwd, opts.SubProvider, opts.SubModel, opts.Store, opts.SubOutput, opts.SubApproval))
	}
	reg.Register(NewExaSearchTool())
	return reg
}