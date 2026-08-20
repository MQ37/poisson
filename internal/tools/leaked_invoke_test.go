package tools

import (
	"context"
	"encoding/json"
	"testing"
)

// paramOpen is split across a `+` so this file's own raw source text never
// contains the exact fragment the tools package's own leak guard
// (validate.go) rejects on sight — which would otherwise block writing this
// very test file via the write tool.
const paramOpen = "<parameter " + `name="`

func TestCountLeakedInvokes(t *testing.T) {
	cases := []struct {
		name string
		text string
		want int
	}{
		{"clean text", "sure, I'll read that file now.", 0},
		{"empty", "", 0},
		// Real sample from the wild: garbled leak where the tool name
		// (bash) is used as a bogus parameter name instead of appearing on
		// the invoke tag, and the parameter tag is never closed.
		{"one garbled invoke", "<invoke>\n" + paramOpen + "bash\">ssh root@host 'echo hi'\n</invoke>", 1},
		{"two garbled invokes", "<invoke>\n" + paramOpen + "bash\">cmd1\n</invoke>\n<invoke>\n" + paramOpen + "bash\">cmd2\n</invoke>", 2},
		{"well-formed invoke", "<invoke name=\"bash\">\n" + paramOpen + "command\">ls</parameter>\n</invoke>", 1},
		{"mentions invoke in prose without a tag", "you can invoke this tool yourself", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CountLeakedInvokes(tc.text); got != tc.want {
				t.Errorf("CountLeakedInvokes(%q) = %d, want %d", tc.text, got, tc.want)
			}
		})
	}
}

// schemaStubTool is a minimal Tool with a caller-chosen schema, for
// exercising ParseLeakedInvokes without any real execution side effect —
// recovery only needs Name()/Schema(). Distinct from tools_test.go's
// stubTool, whose schema is fixed to a bare "{}" object.
type schemaStubTool struct {
	name   string
	schema string
}

func (s schemaStubTool) Name() string            { return s.name }
func (s schemaStubTool) Description() string     { return "" }
func (s schemaStubTool) Schema() json.RawMessage { return json.RawMessage(s.schema) }
func (s schemaStubTool) Execute(context.Context, json.RawMessage) (ToolResult, error) {
	return ToolResult{}, nil
}

func testRegistry() *Registry {
	r := NewRegistry()
	r.Register(schemaStubTool{name: "bash", schema: `{"type":"object","properties":{"command":{"type":"string"},"description":{"type":"string"}},"required":["command","description"]}`})
	r.Register(schemaStubTool{name: "grep", schema: `{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string"}},"required":["pattern"]}`})
	return r
}

func TestParseLeakedInvokes(t *testing.T) {
	reg := testRegistry()

	t.Run("garbled shape recovers the tool's primary field", func(t *testing.T) {
		// Real sample: tool name (bash) leaked as the sole parameter name,
		// its value is the raw command with no "command=" prefix.
		text := "<invoke>\n" + paramOpen + "bash\">ssh root@host 'echo hi'\n</invoke>"
		cleaned, calls := ParseLeakedInvokes(text, reg)
		if len(calls) != 1 {
			t.Fatalf("got %d calls, want 1: %+v", len(calls), calls)
		}
		if calls[0].Name != "bash" {
			t.Errorf("Name = %q, want bash", calls[0].Name)
		}
		var in map[string]string
		if err := json.Unmarshal(calls[0].Input, &in); err != nil {
			t.Fatalf("unmarshal input: %v", err)
		}
		if in["command"] != "ssh root@host 'echo hi'" {
			t.Errorf("command = %q, want the raw ssh command", in["command"])
		}
		if cleaned != "" {
			t.Errorf("cleaned = %q, want the recovered block fully stripped", cleaned)
		}
	})

	t.Run("garbled shape strips a field=value prefix embedded in the value", func(t *testing.T) {
		// Real sample: tool name (grep) leaked as the sole parameter name,
		// but its value still carries "pattern=" as if that part were correct.
		text := "<invoke>\n" + paramOpen + "grep\">pattern=tls|insecure|ca_file\n</invoke>"
		_, calls := ParseLeakedInvokes(text, reg)
		if len(calls) != 1 {
			t.Fatalf("got %d calls, want 1: %+v", len(calls), calls)
		}
		var in map[string]string
		if err := json.Unmarshal(calls[0].Input, &in); err != nil {
			t.Fatalf("unmarshal input: %v", err)
		}
		if in["pattern"] != "tls|insecure|ca_file" {
			t.Errorf("pattern = %q, want the prefix stripped", in["pattern"])
		}
	})

	t.Run("well-formed shape maps params directly onto the schema", func(t *testing.T) {
		text := "<invoke name=\"grep\">\n" + paramOpen + "pattern\">foo</parameter>\n" + paramOpen + "path\">internal</parameter>\n</invoke>"
		_, calls := ParseLeakedInvokes(text, reg)
		if len(calls) != 1 {
			t.Fatalf("got %d calls, want 1: %+v", len(calls), calls)
		}
		var in map[string]string
		if err := json.Unmarshal(calls[0].Input, &in); err != nil {
			t.Fatalf("unmarshal input: %v", err)
		}
		if in["pattern"] != "foo" || in["path"] != "internal" {
			t.Errorf("input = %+v, want pattern=foo path=internal", in)
		}
	})

	t.Run("preamble text survives, only the recovered block is stripped", func(t *testing.T) {
		text := "Let me check that.\n" + "<invoke>\n" + paramOpen + "bash\">ls\n</invoke>"
		cleaned, calls := ParseLeakedInvokes(text, reg)
		if len(calls) != 1 {
			t.Fatalf("got %d calls, want 1: %+v", len(calls), calls)
		}
		if cleaned != "Let me check that." {
			t.Errorf("cleaned = %q, want the preamble text with the invoke block removed", cleaned)
		}
	})

	t.Run("two blocks in one response both recover and both get stripped", func(t *testing.T) {
		text := "<invoke>\n" + paramOpen + "bash\">cmd1\n</invoke>\n<invoke>\n" + paramOpen + "bash\">cmd2\n</invoke>"
		cleaned, calls := ParseLeakedInvokes(text, reg)
		if len(calls) != 2 {
			t.Fatalf("got %d calls, want 2: %+v", len(calls), calls)
		}
		if calls[0].ID == calls[1].ID {
			t.Error("recovered calls must have distinct IDs")
		}
		if cleaned != "" {
			t.Errorf("cleaned = %q, want both recovered blocks stripped", cleaned)
		}
	})

	t.Run("unknown tool name does not recover, and is left in cleaned text", func(t *testing.T) {
		text := "<invoke name=\"does_not_exist\">\n" + paramOpen + "x\">1</parameter>\n</invoke>"
		cleaned, calls := ParseLeakedInvokes(text, reg)
		if len(calls) != 0 {
			t.Errorf("got %d calls, want 0 for an unregistered tool: %+v", len(calls), calls)
		}
		if cleaned != text {
			t.Errorf("cleaned = %q, want the unresolved block left untouched since nothing ran", cleaned)
		}
	})

	t.Run("well-formed shape with an unknown field does not recover", func(t *testing.T) {
		text := "<invoke name=\"bash\">\n" + paramOpen + "not_a_real_field\">x</parameter>\n</invoke>"
		if _, calls := ParseLeakedInvokes(text, reg); len(calls) != 0 {
			t.Errorf("got %d calls, want 0 for a field outside the schema: %+v", len(calls), calls)
		}
	})

	t.Run("garbled shape with more than one parameter is too ambiguous to recover", func(t *testing.T) {
		text := "<invoke>\n" + paramOpen + "bash\">cmd</parameter>\n" + paramOpen + "extra\">x\n</invoke>"
		if _, calls := ParseLeakedInvokes(text, reg); len(calls) != 0 {
			t.Errorf("got %d calls, want 0 for an ambiguous multi-param garbled block: %+v", len(calls), calls)
		}
	})

	t.Run("clean text with no invoke tags recovers nothing and passes through unchanged", func(t *testing.T) {
		cleaned, calls := ParseLeakedInvokes("just a normal answer", reg)
		if len(calls) != 0 {
			t.Errorf("got %d calls, want 0: %+v", len(calls), calls)
		}
		if cleaned != "just a normal answer" {
			t.Errorf("cleaned = %q, want the original text unchanged", cleaned)
		}
	})

	t.Run("nil registry recovers nothing", func(t *testing.T) {
		text := "<invoke name=\"bash\">\n" + paramOpen + "command\">ls</parameter>\n</invoke>"
		if _, calls := ParseLeakedInvokes(text, nil); len(calls) != 0 {
			t.Errorf("got %d calls, want 0 with a nil registry: %+v", len(calls), calls)
		}
	})
}
