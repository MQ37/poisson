package tools

import (
	"context"
	"encoding/json"
	"fmt"
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

// Get returns the named tool, or (nil, false) if not registered.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// Definitions returns ToolDef entries for every registered tool, suitable
// for sending to the provider.
func (r *Registry) Definitions() []ToolDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	defs := make([]ToolDef, 0, len(r.tools))
	for _, t := range r.tools {
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
	res, err := t.Execute(ctx, input)
	return TrimToolResult(res), err
}

// ExecuteParallel dispatches all calls concurrently with a sync.WaitGroup.
// Results are returned in the same order as the input calls.
func (r *Registry) ExecuteParallel(ctx context.Context, calls []ToolCall) ([]ToolResult, error) {
	results := make([]ToolResult, len(calls))
	var wg sync.WaitGroup
	for i, call := range calls {
		wg.Add(1)
		go func(idx int, c ToolCall) {
			defer wg.Done()
			res, err := r.Execute(ctx, c.Name, c.Input)
			if err != nil {
				results[idx] = TrimToolResult(ToolResult{Error: err.Error()})
			} else {
				results[idx] = res
			}
		}(i, call)
	}
	wg.Wait()
	return results, nil
}
