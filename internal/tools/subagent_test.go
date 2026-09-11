package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/auth"
	"github.com/mq37/poisson/internal/config"
	"github.com/mq37/poisson/internal/subagent"
)

func alwaysApproveSubagent(_, _, _, _, _ string) (bool, string) { return true, "" }

func alwaysApproveCrossProvider(context.Context, string, string, string) (bool, string) {
	return true, ""
}

func denyCrossProvider(_ context.Context, _, _, _ string) (bool, string) {
	return false, "no thanks"
}

// A subagent defaults to the exact same provider + model as the main
// session — never a SILENT fallback to some other hardcoded model, which
// would change cost/behavior/quality without the user ever choosing it.
// These are regression tests: the tool used to fall back to a hardcoded
// "ollama"/"glm-5.2:cloud" whenever the resolvers were nil or returned "".
// An explicit, human-approved cross-provider override is the one
// deliberate exception — see TestSubagentToolCrossProvider* below.

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
	for _, want := range []string{"claude-opus-5", "claude-sonnet-5", "claude-fable-5-1", "Providers and models"} {
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

// Description() must list every provider poisson knows about — not just the
// one currently active — since a "provider/model" qualified override can
// now target any of them. The main provider (anthropic here) is tagged
// auto-run; every other provider is tagged approval-required, and an
// unconfigured one additionally says so.
func TestSubagentToolDescriptionListsEveryProvider(t *testing.T) {
	tool := newAnthropicSonnetTool()
	tool.SetConfigFn(func() *config.Config { return config.DefaultConfig() })
	desc := tool.Description()
	for _, want := range []string{
		"anthropic (current provider — auto-runs",
		"xai (different provider, NOT CONFIGURED",
		"grok-build",
		"openai (different provider, NOT CONFIGURED",
		"gpt-5.6-terra",
	} {
		if !strings.Contains(desc, want) {
			t.Errorf("Description() missing %q:\n%s", want, desc)
		}
	}
}

// FormatModelsForPrompt (surfaced through Description()) must show each
// model's actual effort support, not just the blanket effort-scale guide —
// xai/grok-build has none at all.
func TestSubagentToolDescriptionShowsEffortLevels(t *testing.T) {
	tool := newAnthropicSonnetTool()
	tool.SetConfigFn(func() *config.Config { return config.DefaultConfig() })
	desc := tool.Description()
	if !strings.Contains(desc, "claude-sonnet-5: ") || !strings.Contains(desc, "(effort: low/medium/high/xhigh/max)") {
		t.Errorf("Description() missing claude-sonnet-5's effort levels:\n%s", desc)
	}
	if !strings.Contains(desc, "grok-build: ") || !strings.Contains(desc, "(effort: no effort override)") {
		t.Errorf("Description() missing grok-build's no-effort marker:\n%s", desc)
	}
}

// --- cross-provider override ---

func TestSubagentToolCrossProviderRequiresApprovalFn(t *testing.T) {
	tool := newAnthropicSonnetTool() // SetCrossProviderApprovalFn never called
	tool.SetConfigFn(func() *config.Config { return config.DefaultConfig() })
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"x","model":"xai/grok-build"}`))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if !strings.Contains(res.Error, "approval isn't wired") {
		t.Fatalf("expected an approval-not-wired error, got %+v", res)
	}
}

func TestSubagentToolCrossProviderRejectsUnconfiguredProviderBeforeAsking(t *testing.T) {
	tool := newAnthropicSonnetTool()
	tool.SetConfigFn(func() *config.Config { return config.DefaultConfig() })
	asked := false
	tool.SetCrossProviderApprovalFn(func(ctx context.Context, a, b, c string) (bool, string) {
		asked = true
		return true, ""
	})
	// xai has no auth entry and no SetAuth call here — unconfigured.
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"x","model":"xai/grok-build"}`))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if !strings.Contains(res.Error, "not configured") {
		t.Fatalf("expected a not-configured error, got %+v", res)
	}
	if asked {
		t.Error("must not ask a human to approve a spawn that's guaranteed to fail (unconfigured provider)")
	}
}

// End-to-end (real spawned process, fake "child" script, zero LLM calls —
// same technique as subagent_e2e_test.go): an approved cross-provider
// override actually spawns the child with POISSON_SUBAGENT_PROVIDER/MODEL
// set to the OVERRIDE, not the main session's own provider/model.
func TestSubagentToolCrossProviderApprovedSpawnsOnOtherProvider(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	envDump := dir + "/env.txt"
	scriptPath := dir + "/fake-child.sh"
	script := "#!/bin/sh\n" +
		"env | grep '^POISSON_SUBAGENT_' > " + envDump + "\n" +
		"printf '{\"type\":\"done\",\"success\":true}\\n'\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake child script: %v", err)
	}
	restore := subagent.SetLookupExecutableForTest(scriptPath)
	defer restore()

	tool := newAnthropicSonnetTool()
	tool.SetConfigFn(func() *config.Config { return config.DefaultConfig() })
	tool.SetAuth(auth.AuthStore{"xai": {Type: "oauth"}})
	tool.SetCrossProviderApprovalFn(alwaysApproveCrossProvider)
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"x","model":"xai/grok-build"}`))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("Execute reported an error: %q (content=%q)", res.Error, res.Content)
	}
	job := waitForJob(t, tool, jobIDFromAck(t, res.Content), 2*time.Second)
	if job.result.Error != "" {
		t.Fatalf("job reported an error: %q (content=%q)", job.result.Error, job.result.Content)
	}
	if !strings.Contains(job.result.Content, "Ran on xai/grok-build") {
		t.Errorf("result should record it ran on the overridden provider/model, got: %q", job.result.Content)
	}
	env, err := os.ReadFile(envDump)
	if err != nil {
		t.Fatalf("read env dump: %v", err)
	}
	envStr := string(env)
	if !strings.Contains(envStr, "POISSON_SUBAGENT_PROVIDER=xai") {
		t.Errorf("child env missing POISSON_SUBAGENT_PROVIDER=xai:\n%s", envStr)
	}
	if !strings.Contains(envStr, "POISSON_SUBAGENT_MODEL=grok-build") {
		t.Errorf("child env missing POISSON_SUBAGENT_MODEL=grok-build:\n%s", envStr)
	}
}

func TestSubagentToolCrossProviderDeniedBlocksSpawn(t *testing.T) {
	tool := newAnthropicSonnetTool()
	tool.SetConfigFn(func() *config.Config { return config.DefaultConfig() })
	tool.SetAuth(auth.AuthStore{"xai": {Type: "oauth"}})
	tool.SetCrossProviderApprovalFn(denyCrossProvider)
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"x","model":"xai/grok-build"}`))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if !strings.Contains(res.Error, "denied") || !strings.Contains(res.Error, "no thanks") {
		t.Fatalf("expected a denial error carrying the human's reason, got %+v", res)
	}
}

func TestSubagentToolCrossProviderUnknownModelFailsBeforeApproval(t *testing.T) {
	tool := newAnthropicSonnetTool()
	tool.SetConfigFn(func() *config.Config { return config.DefaultConfig() })
	tool.SetAuth(auth.AuthStore{"xai": {Type: "oauth"}})
	asked := false
	tool.SetCrossProviderApprovalFn(func(ctx context.Context, a, b, c string) (bool, string) {
		asked = true
		return true, ""
	})
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"x","model":"xai/grok-nonexistent-9"}`))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if !strings.Contains(res.Error, "unknown model") {
		t.Fatalf("expected an unknown-model error, got %+v", res)
	}
	if asked {
		t.Error("must not ask a human to approve an unknown model")
	}
}

// A same-provider override must never call the cross-provider approval
// function at all — the classic "new gate fires when it shouldn't" bug.
// Uses a real fake-child spawn (not just "does validation pass") so it also
// proves the override reaches Spawn at all — wait via waitForJob before
// returning so the background job is confirmed spawned before this test's
// deferred restore() reverts the fixture (see waitForJobSpawned's doc
// comment for why that ordering matters).
func TestSubagentToolSameProviderOverrideNeverAsksCrossProviderApproval(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	scriptPath := dir + "/fake-child-same-provider.sh"
	script := "#!/bin/sh\nprintf '{\"type\":\"done\",\"success\":true}\\n'\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake child script: %v", err)
	}
	restore := subagent.SetLookupExecutableForTest(scriptPath)
	defer restore()

	tool := newAnthropicSonnetTool()
	tool.SetConfigFn(func() *config.Config { return config.DefaultConfig() })
	asked := false
	tool.SetCrossProviderApprovalFn(func(ctx context.Context, a, b, c string) (bool, string) {
		asked = true
		return true, ""
	})
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"x","model":"claude-opus-5"}`))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if asked {
		t.Error("same-provider override must never invoke the cross-provider approval gate")
	}
	if strings.Contains(res.Error, "denied") || strings.Contains(res.Error, "not configured") {
		t.Fatalf("same-provider override should reach Spawn cleanly, got %+v", res)
	}
	job := waitForJob(t, tool, jobIDFromAck(t, res.Content), 2*time.Second)
	if job.result.Error != "" {
		t.Fatalf("same-provider override should reach Spawn cleanly, job errored: %q", job.result.Error)
	}
}

// A model ID whose own first path segment isn't a real provider (llamacpp's
// naming convention) must resolve against the main provider, not be
// misparsed as a cross-provider request. Real fake-child spawn, same
// waitForJob discipline as the sibling test above.
func TestSubagentToolModelWithSlashButNoProviderPrefixStaysSameProvider(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	scriptPath := dir + "/fake-child-slash-model.sh"
	script := "#!/bin/sh\nprintf '{\"type\":\"done\",\"success\":true}\\n'\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake child script: %v", err)
	}
	restore := subagent.SetLookupExecutableForTest(scriptPath)
	defer restore()

	tool := NewSubagentTool(".", alwaysApproveSubagent)
	tool.SetRuntime(
		func() string { return "llamacpp" },
		func() string { return "unsloth/Laguna-S-2.1-GGUF" },
		func() string { return "" },
	)
	tool.SetConfigFn(func() *config.Config { return config.DefaultConfig() })
	asked := false
	tool.SetCrossProviderApprovalFn(func(ctx context.Context, a, b, c string) (bool, string) {
		asked = true
		return true, ""
	})
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"x","model":"unsloth/Qwen3.6-27B-MTP-GGUF"}`))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if asked {
		t.Error("a model ID whose prefix isn't a real provider must not trigger cross-provider approval")
	}
	waitForJob(t, tool, jobIDFromAck(t, res.Content), 2*time.Second)
	if strings.Contains(res.Error, "unknown model") || strings.Contains(res.Error, "not configured") {
		t.Fatalf("expected the bare llamacpp model to resolve against the main provider, got %+v", res)
	}
}

// --- [subagent] trusted_providers ---

func trustingConfig(ids ...string) *config.Config {
	cfg := config.DefaultConfig()
	cfg.Subagent.TrustedProviders = ids
	return cfg
}

// A trusted pair must still hit the unconfigured-provider check — trust
// skips only the human-approval call, nothing that runs before it.
func TestSubagentToolTrustedPairStillRejectsUnconfiguredProvider(t *testing.T) {
	tool := newAnthropicSonnetTool()
	tool.SetConfigFn(func() *config.Config { return trustingConfig("anthropic", "xai") })
	asked := false
	tool.SetCrossProviderApprovalFn(func(ctx context.Context, a, b, c string) (bool, string) {
		asked = true
		return true, ""
	})
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"x","model":"xai/grok-build"}`))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if !strings.Contains(res.Error, "not configured") {
		t.Fatalf("expected a not-configured error, got %+v", res)
	}
	if asked {
		t.Error("must not ask a human to approve a spawn that's guaranteed to fail (unconfigured provider), even trusted")
	}
}

// A trusted pair must still hit the unknown-model check.
func TestSubagentToolTrustedPairStillRejectsUnknownModel(t *testing.T) {
	tool := newAnthropicSonnetTool()
	tool.SetConfigFn(func() *config.Config { return trustingConfig("anthropic", "xai") })
	tool.SetAuth(auth.AuthStore{"xai": {Type: "oauth"}})
	asked := false
	tool.SetCrossProviderApprovalFn(func(ctx context.Context, a, b, c string) (bool, string) {
		asked = true
		return true, ""
	})
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"x","model":"xai/grok-nonexistent-9"}`))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if !strings.Contains(res.Error, "unknown model") {
		t.Fatalf("expected an unknown-model error, got %+v", res)
	}
	if asked {
		t.Error("must not ask a human to approve an unknown model, even trusted")
	}
}

// The actual regression this feature exists to create: a trusted pair,
// both configured, valid model — spawns WITHOUT ever calling
// crossProviderApprovalFn, and says so in the spawn ack.
func TestSubagentToolTrustedPairSpawnsWithoutApproval(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	scriptPath := dir + "/fake-child-trusted.sh"
	script := "#!/bin/sh\nprintf '{\"type\":\"done\",\"success\":true}\\n'\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake child script: %v", err)
	}
	restore := subagent.SetLookupExecutableForTest(scriptPath)
	defer restore()

	tool := newAnthropicSonnetTool()
	tool.SetConfigFn(func() *config.Config { return trustingConfig("anthropic", "xai") })
	tool.SetAuth(auth.AuthStore{"xai": {Type: "oauth"}})
	asked := false
	tool.SetCrossProviderApprovalFn(func(ctx context.Context, a, b, c string) (bool, string) {
		asked = true
		return true, ""
	})
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"x","model":"xai/grok-build"}`))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("Execute reported an error: %q", res.Error)
	}
	if asked {
		t.Error("a trusted pair must never call crossProviderApprovalFn")
	}
	if !strings.Contains(res.Content, "trusted by config") {
		t.Errorf("spawn ack should note the approval was skipped by trust, got: %q", res.Content)
	}
	waitForJob(t, tool, jobIDFromAck(t, res.Content), 2*time.Second)
}

// Listing only ONE side of the pair (the target, not the main provider)
// must change nothing — the existing always-ask behavior applies.
func TestSubagentToolPartialTrustListStillAsks(t *testing.T) {
	for _, ids := range [][]string{{"xai"}, {}} {
		tool := newAnthropicSonnetTool()
		tool.SetConfigFn(func() *config.Config { return trustingConfig(ids...) })
		tool.SetAuth(auth.AuthStore{"xai": {Type: "oauth"}})
		asked := false
		tool.SetCrossProviderApprovalFn(func(ctx context.Context, a, b, c string) (bool, string) {
			asked = true
			return true, ""
		})
		res, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"x","model":"xai/grok-build"}`))
		if err != nil {
			t.Fatalf("Execute returned a Go error: %v", err)
		}
		if res.Error != "" {
			t.Fatalf("Execute reported an error: %q (trust=%v)", res.Error, ids)
		}
		if !asked {
			t.Errorf("a partial/empty trust list (%v) must still ask", ids)
		}
	}
}

// A trusted CUSTOM provider (not a built-in) must skip approval the same
// way. Without the [models.<name>.<model>] override, MergedModelSettings
// would return ok=false and the spawn would die at the unknown-model check
// before trust is ever consulted — this fixture includes it deliberately.
func TestSubagentToolTrustedCustomProviderSkipsApproval(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	scriptPath := dir + "/fake-child-custom-trusted.sh"
	script := "#!/bin/sh\nprintf '{\"type\":\"done\",\"success\":true}\\n'\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake child script: %v", err)
	}
	restore := subagent.SetLookupExecutableForTest(scriptPath)
	defer restore()

	tool := newAnthropicSonnetTool()
	tool.SetConfigFn(func() *config.Config {
		cfg := trustingConfig("anthropic", "bastion")
		cfg.CustomProviders["bastion"] = &config.CustomProviderConfig{Type: "ollama", BaseURL: "http://bastion:11434"}
		if cfg.ModelOverrides["bastion"] == nil {
			cfg.ModelOverrides["bastion"] = map[string]config.ModelOverride{}
		}
		cfg.ModelOverrides["bastion"]["laguna-s-2.1:q4_K_M"] = config.ModelOverride{ContextWindow: 262144}
		return cfg
	})
	asked := false
	tool.SetCrossProviderApprovalFn(func(ctx context.Context, a, b, c string) (bool, string) {
		asked = true
		return true, ""
	})
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"x","model":"bastion/laguna-s-2.1:q4_K_M"}`))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("Execute reported an error: %q", res.Error)
	}
	if asked {
		t.Error("a trusted custom provider must never call crossProviderApprovalFn")
	}
	waitForJob(t, tool, jobIDFromAck(t, res.Content), 2*time.Second)
}

// Description() must render the new "mutually trusted" tag for a provider
// in the trust set with mainProv, while an untrusted provider keeps the
// original "requires human approval" tag in the SAME call — and a
// trusted-but-UNCONFIGURED provider must render NOT CONFIGURED, never the
// trusted tag (Execute() rejects it regardless of trust).
func TestSubagentToolDescriptionTagsTrustedProvider(t *testing.T) {
	tool := newAnthropicSonnetTool()
	tool.SetConfigFn(func() *config.Config { return trustingConfig("anthropic", "xai") })
	tool.SetAuth(auth.AuthStore{"xai": {Type: "oauth"}})
	desc := tool.Description()
	if !strings.Contains(desc, "xai (different provider, mutually trusted") {
		t.Errorf("Description() missing trusted tag for xai:\n%s", desc)
	}
	if !strings.Contains(desc, "openai (different provider, NOT CONFIGURED") {
		t.Errorf("Description() should keep openai untagged as trusted (not in the list):\n%s", desc)
	}

	// Second sub-case: openai IS in the trust list but has no credentials —
	// must still render NOT CONFIGURED, never the trusted tag.
	tool2 := newAnthropicSonnetTool()
	tool2.SetConfigFn(func() *config.Config { return trustingConfig("anthropic", "openai") })
	desc2 := tool2.Description()
	if !strings.Contains(desc2, "openai (different provider, NOT CONFIGURED") {
		t.Errorf("trusted-but-unconfigured provider must render NOT CONFIGURED, not the trusted tag:\n%s", desc2)
	}
	if strings.Contains(desc2, "openai (different provider, mutually trusted") {
		t.Errorf("trusted-but-unconfigured provider must never render the trusted tag:\n%s", desc2)
	}
}
