package tools

import (
	"context"
	"encoding/json"
	"testing"
)

type resolverStub struct{ backend string }

func (r *resolverStub) Name() string        { return "stub" }
func (r *resolverStub) Description() string { return "" }
func (r *resolverStub) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (r *resolverStub) ResolveDefaultProvider() string { return r.backend }
func (r *resolverStub) Execute(context.Context, json.RawMessage) (ToolResult, error) {
	return ToolResult{}, nil
}

func newResolverRegistry(backend string) *Registry {
	reg := NewRegistry()
	reg.Register(&resolverStub{backend: backend})
	reg.Register(NewBatchTool(reg))
	return reg
}

func TestInjectResolvedProvider_FillsOmittedField(t *testing.T) {
	reg := newResolverRegistry("grok")
	got := InjectResolvedProvider(reg, "stub", json.RawMessage(`{"query":"q"}`))
	var out struct {
		Query    string `json:"query"`
		Provider string `json:"provider"`
	}
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Provider != "grok" || out.Query != "q" {
		t.Errorf("got %s, want provider=grok, query preserved", got)
	}
}

func TestInjectResolvedProvider_LeavesExplicitAlone(t *testing.T) {
	reg := newResolverRegistry("grok")
	input := json.RawMessage(`{"query":"q","provider":"exa"}`)
	got := InjectResolvedProvider(reg, "stub", input)
	if string(got) != string(input) {
		t.Errorf("got %s, want unchanged %s", got, input)
	}
}

func TestInjectResolvedProvider_NoOpForUnknownTool(t *testing.T) {
	reg := newResolverRegistry("grok")
	input := json.RawMessage(`{"query":"q"}`)
	got := InjectResolvedProvider(reg, "not-registered", input)
	if string(got) != string(input) {
		t.Errorf("got %s, want unchanged %s", got, input)
	}
}

func TestInjectResolvedProvider_NoOpForNonResolverTool(t *testing.T) {
	reg := NewRegistry()
	reg.Register(NewReadTool(t.TempDir(), func(context.Context, string, string, string) (bool, string) { return true, "" }))
	input := json.RawMessage(`{"path":"x"}`)
	got := InjectResolvedProvider(reg, "read", input)
	if string(got) != string(input) {
		t.Errorf("got %s, want unchanged %s", got, input)
	}
}

func TestInjectResolvedProvider_EmptyResolverIsNoOp(t *testing.T) {
	reg := newResolverRegistry("") // e.g. a tool with no backend available at all
	input := json.RawMessage(`{"query":"q"}`)
	got := InjectResolvedProvider(reg, "stub", input)
	if string(got) != string(input) {
		t.Errorf("got %s, want unchanged %s", got, input)
	}
}

func TestInjectResolvedProviders_BatchInjectsIntoNestedCalls(t *testing.T) {
	reg := newResolverRegistry("grok")
	input := json.RawMessage(`{"calls":[{"tool":"stub","input":{"query":"q"}},{"tool":"stub","input":{"query":"q2","provider":"exa"}}]}`)
	got := InjectResolvedProviders(reg, "batch", input)

	var parsed struct {
		Calls []struct {
			Input struct {
				Provider string `json:"provider"`
			} `json:"input"`
		} `json:"calls"`
	}
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("unmarshal: %v (input: %s)", err, got)
	}
	if len(parsed.Calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(parsed.Calls))
	}
	if parsed.Calls[0].Input.Provider != "grok" {
		t.Errorf("call 0 provider = %q, want grok", parsed.Calls[0].Input.Provider)
	}
	if parsed.Calls[1].Input.Provider != "exa" {
		t.Errorf("call 1 provider = %q, want exa (explicit, untouched)", parsed.Calls[1].Input.Provider)
	}
}

func TestInjectResolvedProviders_NonBatchDelegatesToSingle(t *testing.T) {
	reg := newResolverRegistry("grok")
	got := InjectResolvedProviders(reg, "stub", json.RawMessage(`{"query":"q"}`))
	var out struct {
		Provider string `json:"provider"`
	}
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Provider != "grok" {
		t.Errorf("provider = %q, want grok", out.Provider)
	}
}
