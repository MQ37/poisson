package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// schemaWithProps builds a minimal JSON Schema object with the given
// top-level property names, for validator tests that don't need a real tool.
func schemaWithProps(names ...string) json.RawMessage {
	props := map[string]any{}
	for _, n := range names {
		props[n] = map[string]any{"type": "string"}
	}
	schema := map[string]any{"type": "object", "properties": props}
	data, _ := json.Marshal(schema)
	return data
}

func TestValidateToolInput_RejectsUnexpectedKey(t *testing.T) {
	// Real sample pulled from a poisson conversation: model corrupted the
	// edit tool's required "edits" field into a bogus literal key instead of
	// filling it, dropping the real edits entirely.
	schema := schemaWithProps("path", "edits")
	input := json.RawMessage(`{"path":"/tmp/x.go","parameter name":"edits"}`)

	err := validateToolInput(schema, input)
	if err == nil {
		t.Fatal("expected rejection of unexpected key, got nil")
	}
	if got := err.Error(); !strings.Contains(got, `"parameter name"`) {
		t.Errorf("error should name the offending key, got: %s", got)
	}
}

func TestValidateToolInput_RejectsLeakedTemplateInValue(t *testing.T) {
	// Real sample: search tool's "before" field value corrupted with a
	// trailing leaked <parameter name="after"> fragment.
	schema := schemaWithProps("pattern", "before")
	input := json.RawMessage(`{"pattern":"foo","before":"2\">\n<parameter name=\"after\">8"}`)

	err := validateToolInput(schema, input)
	if err == nil {
		t.Fatal("expected rejection of leaked template in value, got nil")
	}
}

func TestValidateToolInput_RejectsTrailingArtifactSuffix(t *testing.T) {
	// Real sample: search pattern silently corrupted with a trailing `">`
	// followed by a stray newline — this one didn't even error in rg, it
	// just silently produced "no matches found".
	schema := schemaWithProps("pattern", "path")
	input := json.RawMessage(`{"pattern":"actor\\(actorId\\)\\.call\\(\"\u003e\n","path":"src"}`)

	if err := validateToolInput(schema, input); err == nil {
		t.Fatal("expected rejection of trailing artifact suffix, got nil")
	}
}

// TestValidateToolInput_AllowsLegitimateHTMLAttributePattern guards against
// a real false positive found in review: a bare `">` or `="` suffix with no
// trailing whitespace is ordinary content when searching HTML/JSX source
// (e.g. `href="`, `<img src="`) — it must never be rejected just because it
// happens to end the same way the leaked artifact does.
func TestValidateToolInput_AllowsLegitimateHTMLAttributePattern(t *testing.T) {
	schema := schemaWithProps("pattern")
	for _, pattern := range []string{`href="`, `class="`, `<img src="`, `data-foo="bar">`} {
		input := mustJSON(t, map[string]string{"pattern": pattern})
		if err := validateToolInput(schema, input); err != nil {
			t.Errorf("pattern %q should be allowed, got: %v", pattern, err)
		}
	}
}

func TestValidateToolInput_AllowsCleanInput(t *testing.T) {
	schema := schemaWithProps("pattern", "path", "before", "after")
	input := json.RawMessage(`{"pattern":"foo|bar","path":"internal","before":2,"after":8}`)

	if err := validateToolInput(schema, input); err != nil {
		t.Errorf("clean input should pass, got: %v", err)
	}
}

func TestValidateToolInput_AllowsPlainEnglishMentioningParameterName(t *testing.T) {
	// Only the exact opening-tag syntax `<parameter name=` (or a trailing
	// `">`/`="`) trips the check — ordinary text that happens to contain the
	// words "parameter name" must still pass.
	schema := schemaWithProps("pattern")
	input := json.RawMessage(`{"pattern":"parameter names should be short"}`)

	if err := validateToolInput(schema, input); err != nil {
		t.Errorf("plain english mentioning 'parameter name' should pass, got: %v", err)
	}
}

func TestValidateToolInput_NoPropertiesSkipsCheck(t *testing.T) {
	schema := json.RawMessage(`{"type":"object"}`)
	input := json.RawMessage(`{"anything":"goes"}`)

	if err := validateToolInput(schema, input); err != nil {
		t.Errorf("schema without properties should skip validation, got: %v", err)
	}
}

func TestRegistryExecute_RejectsCorruptedInputBeforeCalling(t *testing.T) {
	r := NewRegistry()
	r.Register(&recordingTool{})

	input := json.RawMessage(`{"command":"echo hi","parameter name":"description"}`)
	res, err := r.Execute(context.Background(), "record", input)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected an error result for corrupted input")
	}
	if calledTool {
		t.Error("Execute must not have called the underlying tool's Execute")
	}
}

var calledTool bool

type recordingTool struct{}

func (recordingTool) Name() string        { return "record" }
func (recordingTool) Description() string { return "records whether it was called" }
func (recordingTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"},"description":{"type":"string"}}}`)
}
func (recordingTool) Execute(context.Context, json.RawMessage) (ToolResult, error) {
	calledTool = true
	return ToolResult{Content: "ran"}, nil
}
