package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/config"
)

func alwaysApproveSubagent(_, _, _, _, _ string) (bool, string) { return true, "" }

// A subagent must always run the same provider + model as the main session —
// never a silent fallback to some other hardcoded model, which would change
// cost/behavior/quality without the user ever choosing it. These are
// regression tests: the tool used to fall back to a hardcoded
// "ollama"/"glm-5.2:cloud" whenever the resolvers were nil or returned "".

func TestSubagentToolErrorsWhenRuntimeNotConfigured(t *testing.T) {
	tool := NewSubagentTool(".", alwaysApproveSubagent)
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"do something"}`))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected a ToolResult.Error when provider/model resolvers were never wired")
	}
}

func TestSubagentToolErrorsWhenProviderEmpty(t *testing.T) {
	tool := NewSubagentTool(".", alwaysApproveSubagent)
	tool.SetRuntime(func() string { return "" }, func() string { return "some-model" }, func() string { return "" })
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"do something"}`))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if !strings.Contains(res.Error, "provider") {
		t.Fatalf("expected a provider-related error, got %+v", res)
	}
}

func TestSubagentToolErrorsWhenModelEmpty(t *testing.T) {
	tool := NewSubagentTool(".", alwaysApproveSubagent)
	tool.SetRuntime(func() string { return "anthropic" }, func() string { return "" }, func() string { return "" })
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"do something"}`))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if !strings.Contains(res.Error, "model") {
		t.Fatalf("expected a model-related error, got %+v", res)
	}
}

// --- per-subagent model/effort override ---

func newAnthropicSonnetTool() *SubagentTool {
	tool := NewSubagentTool(".", alwaysApproveSubagent)
	tool.SetRuntime(
		func() string { return "anthropic" },
		func() string { return "claude-sonnet-5" },
		func() string { return "" },
	)
	return tool
}

func TestSubagentToolOverrideRejectedWithoutConfigFn(t *testing.T) {
	tool := newAnthropicSonnetTool() // SetConfigFn never called
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"x","model":"claude-opus-5"}`))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if !strings.Contains(res.Error, "not supported in this session") {
		t.Fatalf("expected a config-not-wired error, got %+v", res)
	}
}

func TestSubagentToolOverrideRejectsUnknownModel(t *testing.T) {
	tool := newAnthropicSonnetTool()
	tool.SetConfigFn(func() *config.Config { return nil })
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"x","model":"claude-nonexistent-9"}`))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if !strings.Contains(res.Error, "unknown model") {
		t.Fatalf("expected an unknown-model error, got %+v", res)
	}
}

func TestSubagentToolOverrideRejectsUnsupportedEffort(t *testing.T) {
	tool := newAnthropicSonnetTool()
	tool.SetConfigFn(func() *config.Config { return nil })
	// claude-sonnet-5 doesn't support a "ludicrous" level.
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"x","effort":"ludicrous"}`))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if !strings.Contains(res.Error, "not supported by model") {
		t.Fatalf("expected an unsupported-effort error, got %+v", res)
	}
}

func TestSubagentToolOverrideRejectsEffortOnNoEffortModel(t *testing.T) {
	// xai/grok-build is a known model with SupportsEffort=false and no
	// EffortLevels at all — distinct from "wrong level requested" above,
	// this is "this model has no effort knob whatsoever."
	tool := NewSubagentTool(".", alwaysApproveSubagent)
	tool.SetRuntime(
		func() string { return "xai" },
		func() string { return "grok-build" },
		func() string { return "" },
	)
	tool.SetConfigFn(func() *config.Config { return nil })
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"x","effort":"high"}`))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if !strings.Contains(res.Error, "not supported by model") {
		t.Fatalf("expected an unsupported-effort error, got %+v", res)
	}
}

func TestSubagentToolDescriptionListsProviderModels(t *testing.T) {
	tool := newAnthropicSonnetTool()
	tool.SetConfigFn(func() *config.Config { return nil })
	desc := tool.Description()
	for _, want := range []string{"claude-opus-5", "claude-sonnet-5", "claude-fable-5", "Available models"} {
		if !strings.Contains(desc, want) {
			t.Errorf("Description() missing %q:\n%s", want, desc)
		}
	}
}

func TestSubagentToolDescriptionFallsBackWithoutConfigFn(t *testing.T) {
	tool := newAnthropicSonnetTool() // SetConfigFn never called
	if got := tool.Description(); got != subagentBaseDescription {
		t.Errorf("Description() = %q, want the static base text unchanged", got)
	}
}
