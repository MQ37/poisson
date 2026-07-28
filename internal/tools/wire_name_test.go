package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// echoTool is a trivial registered tool used to check name resolution.
type echoTool struct{ name string }

func (t echoTool) Name() string            { return t.name }
func (t echoTool) Description() string     { return "echo, for tests" }
func (t echoTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t echoTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	return ToolResult{Content: "ran " + t.name}, nil
}

// TestGetResolvesWireToolName covers the failure this fixes: the Anthropic
// stealth path advertises tools as mcp_Bash/mcp_Grep, and a model that echoes
// that wire spelling back must still reach the registered tool.
func TestGetResolvesWireToolName(t *testing.T) {
	r := NewRegistry()
	r.Register(echoTool{name: "grep"})
	r.Register(echoTool{name: "web_ask"})

	for _, name := range []string{"grep", "mcp_Grep", "mcp_grep", "mcp_Web_ask"} {
		got, ok := r.Get(name)
		if !ok {
			t.Fatalf("Get(%q) not found, want the registered tool", name)
		}
		if name == "mcp_Web_ask" && got.Name() != "web_ask" {
			t.Fatalf("Get(%q) = %q, want web_ask", name, got.Name())
		}
	}

	if _, ok := r.Get("mcp_Nope"); ok {
		t.Fatalf("Get(mcp_Nope) found a tool, want miss")
	}
}

// TestExecuteResolvesWireToolName is the dispatch half: a wire-named call runs
// the tool instead of coming back as "tool not registered".
func TestExecuteResolvesWireToolName(t *testing.T) {
	r := NewRegistry()
	r.Register(echoTool{name: "grep"})

	res, err := r.Execute(context.Background(), "mcp_Grep", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute(mcp_Grep) err = %v, want nil", err)
	}
	if res.Content != "ran grep" {
		t.Fatalf("res.Content = %q, want %q", res.Content, "ran grep")
	}
}

// TestExecuteUnknownWireNameReportsWhatModelSent keeps the error message
// honest — an unresolvable name must not be silently rewritten.
func TestExecuteUnknownWireNameReportsWhatModelSent(t *testing.T) {
	r := NewRegistry()
	res, _ := r.Execute(context.Background(), "mcp_Nope", json.RawMessage(`{}`))
	if !strings.Contains(res.Error, "mcp_Nope") {
		t.Fatalf("res.Error = %q, want it to name mcp_Nope", res.Error)
	}
}

// TestBatchResolvesWireToolNames drives the original report: batch's
// calls[].tool is opaque JSON to the provider layer, so a wire name in there
// used to fail validation before anything ran.
func TestBatchResolvesWireToolNames(t *testing.T) {
	r := NewRegistry()
	r.Register(echoTool{name: "grep"})
	b := NewBatchTool(r)
	r.Register(b)

	in := json.RawMessage(`{"calls":[{"tool":"mcp_Grep","input":{}},{"tool":"grep","input":{}}]}`)
	res, err := b.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("batch err = %v", err)
	}
	if res.Error != "" {
		t.Fatalf("batch res.Error = %q, want no error", res.Error)
	}
	if strings.Count(res.Content, "ran grep") != 2 {
		t.Fatalf("res.Content = %q, want both calls to run grep", res.Content)
	}
}

// TestBatchDeniesWireNamedBatch closes the hole the same normalization opens:
// the no-recursion deny list keys off "batch", so "mcp_Batch" must be
// canonicalized BEFORE the deny check, not after it.
func TestBatchDeniesWireNamedBatch(t *testing.T) {
	r := NewRegistry()
	b := NewBatchTool(r)
	r.Register(b)

	res, _ := b.Execute(context.Background(), json.RawMessage(`{"calls":[{"tool":"mcp_Batch","input":{}}]}`))
	if !strings.Contains(res.Error, "not allowed inside batch") {
		t.Fatalf("res.Error = %q, want the recursion denial", res.Error)
	}
}
