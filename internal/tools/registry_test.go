package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// panickingTool is a minimal Tool whose Execute always panics — used to
// simulate a real tool bug (nil pointer, index out of range, etc.) without
// needing to actually trigger one in a real tool implementation.
type panickingTool struct{ value interface{} }

func (t panickingTool) Name() string            { return "panicky" }
func (t panickingTool) Description() string     { return "always panics, for tests" }
func (t panickingTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t panickingTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	panic(t.value)
}

// TestExecuteRecoversPanickingTool covers the crash this session traced: a
// subagent's tool call died with a bare "EOF" reaching the parent (the child
// process crashed unrecovered), with zero information about why. Registry.
// Execute must never let a panicking tool crash its caller — it should come
// back as an ordinary ToolResult error instead, so the turn (and the whole
// process, interactive or subagent) keeps running.
func TestExecuteRecoversPanickingTool(t *testing.T) {
	r := NewRegistry()
	r.Register(panickingTool{value: "boom: index out of range"})

	res, err := r.Execute(context.Background(), "panicky", json.RawMessage(`{}`))

	if err != nil {
		t.Fatalf("Execute returned a Go error %v, want nil (panic must become a ToolResult error)", err)
	}
	if res.Error == "" {
		t.Fatalf("res.Error empty, want a message describing the panic")
	}
	if !strings.Contains(res.Error, "panicky") || !strings.Contains(res.Error, "boom: index out of range") {
		t.Fatalf("res.Error = %q, want it to name the tool and the panic value", res.Error)
	}
}

// TestExecuteRecoversNonStringPanicValue guards against a naive %s-only
// formatter: real panics are often errors or arbitrary values, not strings
// (e.g. runtime.Error for a genuine nil dereference or index-out-of-range).
func TestExecuteRecoversNonStringPanicValue(t *testing.T) {
	r := NewRegistry()
	r.Register(panickingTool{value: errIndexOutOfRange{}})

	res, err := r.Execute(context.Background(), "panicky", json.RawMessage(`{}`))

	if err != nil {
		t.Fatalf("Execute returned a Go error %v, want nil", err)
	}
	if !strings.Contains(res.Error, "synthetic index out of range") {
		t.Fatalf("res.Error = %q, want it to include the panic value's message", res.Error)
	}
}

type errIndexOutOfRange struct{}

func (errIndexOutOfRange) Error() string { return "synthetic index out of range" }

// prettySchemaTool has a hand-indented, multi-line Schema() — same style
// every real tool uses for source readability — to verify Definitions()
// strips that formatting before it goes out over the wire.
type prettySchemaTool struct{}

func (prettySchemaTool) Name() string        { return "pretty" }
func (prettySchemaTool) Description() string { return "d" }
func (prettySchemaTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "x": { "type": "string" }
  }
}`)
}
func (prettySchemaTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	return ToolResult{}, nil
}

// TestDefinitionsCompactsSchema guards the wire-size fix: Schema() literals
// are pretty-printed in source, but json.RawMessage copies bytes verbatim
// into every provider's request — indentation whitespace used to ride along
// on every single API call for zero semantic value.
func TestDefinitionsCompactsSchema(t *testing.T) {
	r := NewRegistry()
	r.Register(prettySchemaTool{})

	defs := r.Definitions()
	if len(defs) != 1 {
		t.Fatalf("want 1 definition, got %d", len(defs))
	}
	got := string(defs[0].Schema)
	if strings.Contains(got, "\n") || strings.Contains(got, "  ") {
		t.Errorf("schema not compacted: %q", got)
	}
	want := `{"type":"object","properties":{"x":{"type":"string"}}}`
	if got != want {
		t.Errorf("schema = %q, want %q", got, want)
	}
}

// malformedSchemaTool has invalid JSON in Schema(), simulating a typo in a
// hand-written schema literal.
type malformedSchemaTool struct{}

func (malformedSchemaTool) Name() string            { return "broken" }
func (malformedSchemaTool) Description() string     { return "d" }
func (malformedSchemaTool) Schema() json.RawMessage { return json.RawMessage(`{not valid json`) }
func (malformedSchemaTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	return ToolResult{}, nil
}

// TestDefinitionsKeepsMalformedSchemaAsIs guards the fail-open fallback: a
// bug in one tool's hand-written schema literal must not drop that tool from
// the request entirely.
func TestDefinitionsKeepsMalformedSchemaAsIs(t *testing.T) {
	r := NewRegistry()
	r.Register(malformedSchemaTool{})

	defs := r.Definitions()
	if len(defs) != 1 {
		t.Fatalf("want 1 definition, got %d", len(defs))
	}
	if string(defs[0].Schema) != `{not valid json` {
		t.Errorf("malformed schema should pass through unchanged, got %q", defs[0].Schema)
	}
}

// TestExecuteStillWorksForNormalTools is a sanity check that adding the
// recover() didn't change ordinary success/error behavior.
func TestExecuteStillWorksForNormalTools(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.Get("read"); ok {
		t.Fatalf("registry should start empty")
	}
	res, err := r.Execute(context.Background(), "does-not-exist", json.RawMessage(`{}`))
	if err == nil {
		t.Fatalf("want an error for an unregistered tool")
	}
	if res.Error == "" {
		t.Fatalf("want res.Error set for an unregistered tool")
	}
}
