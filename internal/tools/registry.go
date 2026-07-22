package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

// Registry holds all registered tools, keyed by tool name.
type Registry struct {
	tools map[string]Tool
	mu    sync.RWMutex
}

// NewRegistry creates an empty tool registry.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register adds a tool to the registry, keyed by its Name().
func (r *Registry) Register(tool Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[tool.Name()] = tool
}

// Unregister removes a tool by name. No-op if missing.
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tools, name)
}

// Get returns the named tool, or (nil, false) if not registered.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// Definitions returns ToolDef entries for every registered tool, suitable
// for sending to the provider. Tools are ordered by name so the serialized
// request is byte-stable across turns — ranging a Go map is randomized, which
// reshuffles the tools array every request and breaks Anthropic prompt caching
// (the tools prefix hash changes, forcing a full cache write instead of a read).
func (r *Registry) Definitions() []ToolDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	defs := make([]ToolDef, 0, len(names))
	for _, name := range names {
		t := r.tools[name]
		defs = append(defs, ToolDef{
			Name:        t.Name(),
			Description: t.Description(),
			Schema:      t.Schema(),
		})
	}
	return defs
}

// Execute dispatches a single tool call by name.
func (r *Registry) Execute(ctx context.Context, name string, input json.RawMessage) (ToolResult, error) {
	r.mu.RLock()
	t, ok := r.tools[name]
	r.mu.RUnlock()
	if !ok {
		res := TrimToolResult(ToolResult{Error: "tool not registered: " + name})
		return res, fmt.Errorf("tool not registered: %s", name)
	}
	if err := validateToolInput(t.Schema(), input); err != nil {
		return TrimToolResult(ToolResult{Error: err.Error()}), nil
	}
	res, err := t.Execute(ctx, input)
	return TrimToolResult(res), err
}
