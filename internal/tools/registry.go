package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
)

// wireToolPrefix is the camouflage prefix the Anthropic stealth path puts on
// every advertised tool name (bash -> mcp_Bash, see provider.prefixToolName).
// The provider strips it off incoming tool_use blocks, but it cannot rewrite
// tool names the model buries inside a tool's own arguments — batch's
// calls[].tool is opaque JSON to that layer, so a model echoing the wire name
// there ("mcp_Grep") used to fail with "tool not registered". Resolution
// happens here instead, where every dispatch path passes through.
const wireToolPrefix = "mcp_"

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

// Get returns the named tool, or (nil, false) if not registered. A wire name
// ("mcp_Bash") resolves to the registered tool ("bash").
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[r.resolveName(name)]
	return t, ok
}

// Canonical maps a model-emitted tool name to the registered name, so callers
// that key their own tables off tool names (batch's deny list, its
// mutating-tool set) match on one spelling instead of letting a wire-prefixed
// name slip past. Unknown names pass through unchanged.
func (r *Registry) Canonical(name string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.resolveName(name)
}

// resolveName maps a model-emitted tool name to a registered one. Exact
// matches win; otherwise the wire prefix is stripped and the first letter
// lowercased (mcp_Web_ask -> web_ask), and finally the whole name is
// lowercased. That last step matters for batch: a model that half-strips the
// wire prefix emits "Glob" or "Web_ask" inside calls[].tool, which is neither
// an exact match nor prefixed. Registered names are all lowercase, so a
// lowercase probe is unambiguous. Callers must hold r.mu.
func (r *Registry) resolveName(name string) string {
	for _, candidate := range []string{name, stripWireToolPrefix(name), strings.ToLower(stripWireToolPrefix(name))} {
		if _, ok := r.tools[candidate]; ok {
			return candidate
		}
	}
	return name
}

// CanonicalToolName maps a wire tool name to its bare form (mcp_Bash -> bash)
// without a registry. Registry.Canonical is the dispatch path's version and
// only rewrites names it can actually resolve; display paths (a tool card, a
// batch call list) have no registry at hand and want the bare spelling
// regardless — a model that echoes wire names inside batch's own arguments
// otherwise leaves "mcp_Bash, mcp_Read" on screen.
func CanonicalToolName(name string) string { return strings.ToLower(stripWireToolPrefix(name)) }

// stripWireToolPrefix turns a wire tool name into the bare one
// (mcp_Web_ask -> web_ask). Names without the prefix pass through unchanged.
func stripWireToolPrefix(name string) string {
	rest, ok := strings.CutPrefix(name, wireToolPrefix)
	if !ok || rest == "" {
		return name
	}
	return strings.ToLower(rest[:1]) + rest[1:]
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
			Schema:      compactSchema(t.Schema()),
		})
	}
	return defs
}

// compactSchema strips insignificant whitespace from a tool's JSON schema.
// Schema() literals are hand-indented for source readability, but that
// indentation is sent byte-for-byte over the wire every request (ToolDef.Schema
// is json.RawMessage, copied verbatim by every provider's request marshaling)
// — pure formatting with no semantic value to the model. Falls back to the
// original bytes on malformed JSON so a bug in one tool's schema can't drop it
// from the request entirely.
func compactSchema(schema []byte) []byte {
	var buf bytes.Buffer
	if err := json.Compact(&buf, schema); err != nil {
		return schema
	}
	return buf.Bytes()
}

// Execute dispatches a single tool call by name. A panic inside the tool's
// own Execute (nil pointer, index out of range, etc.) is recovered here and
// turned into an ordinary ToolResult error instead of propagating — every
// caller (the main turn loop, /btw, a subagent child) already handles a
// failed tool call gracefully and keeps going; an unrecovered panic would
// instead crash whichever process is running it (interactive session or
// subagent alike), abandoning the whole conversation over one bad tool call.
// The full stack trace goes to the log; callers only need the short message.
func (r *Registry) Execute(ctx context.Context, name string, input json.RawMessage) (res ToolResult, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("tool %q panicked: %v\n%s", name, rec, debug.Stack())
			res = TrimToolResult(ToolResult{Error: fmt.Sprintf("tool %q panicked: %v", name, rec)})
			err = nil
		}
	}()

	r.mu.RLock()
	name = r.resolveName(name)
	t, ok := r.tools[name]
	r.mu.RUnlock()
	if !ok {
		res = TrimToolResult(ToolResult{Error: "tool not registered: " + name})
		return res, fmt.Errorf("tool not registered: %s", name)
	}
	if verr := validateToolInput(t.Schema(), input); verr != nil {
		return TrimToolResult(ToolResult{Error: verr.Error()}), nil
	}
	res, err = t.Execute(ctx, input)
	return TrimToolResult(res), err
}
