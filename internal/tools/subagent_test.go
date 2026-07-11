package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
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
