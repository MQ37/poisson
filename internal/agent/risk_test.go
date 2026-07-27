package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/store"
)

// TestWrapRiskGatedApproval_NeverAutoApprovesObfuscatedDestructiveCommand is
// the master regression test for the whole point of the destructive-command
// fast path: even against an ADVERSARIAL FakeProvider hard-coded to always
// say "low" (simulating an LLM misclassification, the exact failure mode the
// fast path exists to make impossible), rm-family commands — including
// trivially obfuscated spellings that a naive strings.Fields-based detector
// used to miss (see the fixed isDestructiveCommand/isUntrustedExecCommand/
// isPackageInstallCommand) — must always reach the human, with zero LLM
// calls spent getting there.
func TestWrapRiskGatedApproval_NeverAutoApprovesObfuscatedDestructiveCommand(t *testing.T) {
	cases := []string{
		"rm -rf /",
		"rm -rf /tmp/x",
		`\rm -rf /tmp/x`,
		`sudo \rm -rf /tmp/x`,
		"RM -rf /tmp/x",
		"/bin/rm -rf /tmp/x",
		"find . -delete",
		"find . -exec rm {} \\;",
		`\npx some-evil-package`,
		`\npm install some-evil-package`,
		"r''m -rf /tmp/x", // quote-spliced — real bash for "rm"
		"'r'm -rf /tmp/x",
		`r"m" -rf /tmp/x`,
	}
	for _, cmd := range cases {
		t.Run(cmd, func(t *testing.T) {
			fp := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
			// Adversarial: the LLM always says low, as if compromised,
			// confused by obfuscation, or just wrong. The fast path must
			// never let this matter.
			fp.SetResponses([][]provider.StreamEvent{
				provider.FakeTextResponse("low", nil),
				provider.FakeTextResponse("low", nil),
			})
			s := newTestStore(t)
			sid := newTestSession(t, s, "m")
			a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, nil, nil)
			a.SetModel("m")

			asked := false
			approve := WrapRiskGatedApproval(a, func(_, _, _ string, risk BashRisk, _ ApprovalOrigin) (bool, string) {
				asked = true
				if risk != BashRiskHigh && risk != BashRiskMedium {
					t.Errorf("risk seen by human callback = %q, want high or medium (never low/unknown for a known-dangerous command)", risk)
				}
				return false, "" // the human denies — that's the point, not the interesting part
			})
			_, _ = approve(context.Background(), cmd, "test", "/tmp")

			if !asked {
				t.Fatalf("command %q was auto-approved without ever asking a human — the adversarial LLM's 'low' verdict was allowed to stand", cmd)
			}
			if fp.CallCount() != 0 {
				t.Errorf("command %q spent %d LLM call(s) reaching the human — want 0 (deterministic fast path should short-circuit before any classification)", cmd, fp.CallCount())
			}
		})
	}
}

// TestAssessBashRiskUsesIsolatedContext guards that the bash-risk classifier
// call never carries the conversation: even with a populated session history,
// the request must be a single synthetic user message + the fixed classifier
// system prompt, with none of the prior conversation leaking in.
func TestAssessBashRiskUsesIsolatedContext(t *testing.T) {
	fp := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	fp.SetResponses([][]provider.StreamEvent{
		provider.FakeTextResponse("medium", nil),
		provider.FakeTextResponse("medium", nil),
	})

	s := newTestStore(t)
	sid := newTestSession(t, s, "m")
	const marker = "SECRET_CONVERSATION_MARKER_9137"
	for _, m := range []*store.Message{
		{SessionID: sid, Role: "user", Content: `[{"type":"text","text":"` + marker + ` please help"}]`},
		{SessionID: sid, Role: "assistant", Content: `[{"type":"text","text":"sure, ` + marker + `"}]`},
	} {
		if err := s.AppendMessage(m); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, nil, nil)
	a.SetModel("m")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// A plain build script, not git commit — that's now a fast path
	// (BashRiskHigh without any LLM call, see TestAssessBashRiskGitCommitFastPath)
	// and would never reach the LLM this test needs to inspect.
	a.AssessBashRisk(ctx, "./scripts/build.sh --release", "build release", "/tmp")

	req := fp.LastRequest()
	if req == nil {
		t.Fatal("no risk request captured")
	}
	if len(req.Messages) != 1 || req.Messages[0].Role != "user" {
		t.Fatalf("risk request must be exactly one user message, got %d: %+v", len(req.Messages), req.Messages)
	}
	if len(req.System) != 1 || !strings.Contains(req.System[0].Text, "classify bash command risk") {
		t.Fatalf("risk request system must be the classifier prompt only, got %+v", req.System)
	}
	userText := req.Messages[0].Content[0].Text
	if !strings.Contains(userText, "./scripts/build.sh --release") {
		t.Errorf("risk prompt should contain the command, got %q", userText)
	}
	// The conversation must not appear anywhere in the request.
	if strings.Contains(userText, marker) || strings.Contains(req.System[0].Text, marker) {
		t.Error("conversation history leaked into the bash-risk request")
	}
}

// shrinkRetryBackoffs makes the mid-stream and empty-response retry sleeps
// negligible for the duration of a test, so a test that deliberately trips a
// retry path doesn't pay the real (seconds-long) schedule.
func shrinkRetryBackoffs(t *testing.T) {
	t.Helper()
	oldEmpty, oldMid := emptyResponseBackoff, midStreamErrorBackoff
	emptyResponseBackoff, midStreamErrorBackoff = time.Millisecond, time.Millisecond
	t.Cleanup(func() { emptyResponseBackoff, midStreamErrorBackoff = oldEmpty, oldMid })
}

// TestAssessBashRiskRetriesMidStreamOverload verifies the classifier gets the
// same mid-stream resilience a real turn has: a retryable provider error
// arriving after HTTP 200 (Anthropic's overloaded_error and friends, which
// provider.DoWithRetry structurally cannot see) is retried instead of
// collapsing to "unknown risk" and sending the user to a manual prompt.
func TestAssessBashRiskRetriesMidStreamOverload(t *testing.T) {
	shrinkRetryBackoffs(t)
	fp := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	fp.SetResponses([][]provider.StreamEvent{
		{{Type: provider.EventError, Error: fmt.Errorf("overloaded_error"), Retryable: true}},
		provider.FakeTextResponse("low", nil),
	})

	s := newTestStore(t)
	sid := newTestSession(t, s, "m")
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, nil, nil)
	a.SetModel("m")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if got := a.AssessBashRisk(ctx, "./scripts/build.sh", "build", "/tmp"); got != BashRiskLow {
		t.Fatalf("risk = %q, want low from the retried attempt", got)
	}
	if fp.CallCount() != 2 {
		t.Errorf("provider calls = %d, want 2 (one overload + one retry)", fp.CallCount())
	}
}

// TestAssessBashRiskGivesUpOnPersistentOverload verifies the retry budget is
// bounded: a provider stuck in overload ends as unknown risk (the approval
// gate then asks the human) rather than retrying forever while the user waits.
func TestAssessBashRiskGivesUpOnPersistentOverload(t *testing.T) {
	shrinkRetryBackoffs(t)
	fp := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	overload := []provider.StreamEvent{{Type: provider.EventError, Error: fmt.Errorf("overloaded_error"), Retryable: true}}
	fp.SetResponses([][]provider.StreamEvent{overload, overload, overload, overload, overload})

	s := newTestStore(t)
	sid := newTestSession(t, s, "m")
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, nil, nil)
	a.SetModel("m")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if got := a.AssessBashRisk(ctx, "./scripts/build.sh", "build", "/tmp"); got != BashRiskUnknown {
		t.Fatalf("risk = %q, want unknown after the retry budget runs out", got)
	}
	if want := maxMidStreamErrorRetries + 1; fp.CallCount() != want {
		t.Errorf("provider calls = %d, want %d (initial + %d retries)", fp.CallCount(), want, maxMidStreamErrorRetries)
	}
}

// TestAssessBashRiskRetriesEmptyResponse verifies an empty classifier reply
// (a transient glitch, or a thinking-only model that produced nothing) is
// retried rather than immediately reported as unknown risk.
func TestAssessBashRiskRetriesEmptyResponse(t *testing.T) {
	shrinkRetryBackoffs(t)
	fp := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	fp.SetResponses([][]provider.StreamEvent{
		{{Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 5, OutputTokens: 0}}},
		provider.FakeTextResponse("high", nil),
	})

	s := newTestStore(t)
	sid := newTestSession(t, s, "m")
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, nil, nil)
	a.SetModel("m")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if got := a.AssessBashRisk(ctx, "./scripts/build.sh", "build", "/tmp"); got != BashRiskHigh {
		t.Fatalf("risk = %q, want high from the retried attempt", got)
	}
	if fp.CallCount() != 2 {
		t.Errorf("provider calls = %d, want 2 (one empty + one retry)", fp.CallCount())
	}
}

// TestBashRiskRunTimeoutExceedsTransportAttempt guards the interaction that
// used to silently defeat provider.DoWithRetry inside the classifier: a
// per-round deadline shorter than one transport attempt means the round dies
// before a hung connection can ever be retried.
func TestBashRiskRunTimeoutExceedsTransportAttempt(t *testing.T) {
	if bashRiskRunTimeout <= provider.AttemptTimeout() {
		t.Fatalf("bashRiskRunTimeout %s must exceed provider.AttemptTimeout() %s",
			bashRiskRunTimeout, provider.AttemptTimeout())
	}
}

// TestClassifierModelResolution covers the three layers behind
// ClassifierModel: a per-provider pin (/classifier-model), the
// config.Classifier.Model default, and the session model as fallback.
func TestClassifierModelResolution(t *testing.T) {
	s := newTestStore(t)
	fp := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	sid := newTestSession(t, s, "m")
	cfg := newTestConfig()
	a := NewAgent(s, fp, newTestRegistry("."), cfg, sid, nil, nil)
	a.SetModel("m")

	if got := a.ClassifierModel(); got != "m" {
		t.Errorf("with no config or pin, classifier model = %q, want the session model m", got)
	}
	if a.ClassifierModelPinned() {
		t.Error("nothing pinned yet")
	}

	cfg.Classifier.Model = "cfg-classifier"
	if got := a.ClassifierModel(); got != "cfg-classifier" {
		t.Errorf("classifier model = %q, want the config default", got)
	}

	// A config entry naming another provider must not apply here.
	cfg.Classifier.Model = "other/cfg-classifier"
	if got := a.ClassifierModel(); got != "m" {
		t.Errorf("foreign-provider config entry should be ignored, got %q", got)
	}
	cfg.Classifier.Model = "fake/cfg-classifier"
	if got := a.ClassifierModel(); got != "cfg-classifier" {
		t.Errorf("own-provider config entry should apply, got %q", got)
	}

	a.SetClassifierModel("pinned")
	if got := a.ClassifierModel(); got != "pinned" {
		t.Errorf("pin should win over config, got %q", got)
	}
	if !a.ClassifierModelPinned() {
		t.Error("pin should report as pinned")
	}
	if a.Model() != "m" {
		t.Errorf("session model must be untouched, got %q", a.Model())
	}

	a.SetClassifierModel("")
	if got := a.ClassifierModel(); got != "cfg-classifier" {
		t.Errorf("clearing the pin should fall back to config, got %q", got)
	}
}

// TestClassifierModelUsedInRiskRequest verifies the pinned classifier model
// is what actually reaches the provider for a risk classification, while the
// session model stays in charge of normal turns.
func TestClassifierModelUsedInRiskRequest(t *testing.T) {
	// The bare fake answers with no content, which now (correctly) trips the
	// empty-response retry path — shrink its sleeps instead of paying them.
	shrinkRetryBackoffs(t)
	s := newTestStore(t)
	fp := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	sid := newTestSession(t, s, "m")
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, nil, nil)
	a.SetModel("m")
	a.SetClassifierModel("tiny-classifier")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a.AssessBashRisk(ctx, "./scripts/build.sh --release", "build release", "/tmp")

	req := fp.LastRequest()
	if req == nil {
		t.Fatal("no risk request captured")
	}
	if req.Model != "tiny-classifier" {
		t.Errorf("risk request model = %q, want tiny-classifier", req.Model)
	}
}

// TestClassifierModelConcurrentAccess exercises the case /classifier-model
// deliberately allows: the user pins a classifier model from the TUI
// goroutine while the turn-loop goroutine is classifying commands. Without
// the mutex around classifierModels this is a fatal "concurrent map read and
// map write", not a soft race — run under -race to catch regressions.
func TestClassifierModelConcurrentAccess(t *testing.T) {
	s := newTestStore(t)
	fp := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	sid := newTestSession(t, s, "m")
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, nil, nil)
	a.SetModel("m")

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 500; i++ {
			a.SetClassifierModel("pinned")
			a.SetClassifierModel("")
		}
	}()
	for i := 0; i < 500; i++ {
		_ = a.ClassifierModel()
		_ = a.ClassifierModelPinned()
	}
	<-done
}

func TestParseBashRisk(t *testing.T) {
	cases := []struct {
		in   string
		want BashRisk
	}{
		{"low", BashRiskLow},
		{"HIGH", BashRiskHigh},
		{"  medium\n", BashRiskMedium},
		{"Risk: high", BashRiskHigh},
		{"The risk is medium.", BashRiskMedium},
		{"moderate", BashRiskMedium},
		{`"high"`, BashRiskHigh},
		{"(low)", BashRiskLow},
		{"", BashRiskUnknown},
		{"unclear", BashRiskUnknown},
	}
	for _, tc := range cases {
		if got := ParseBashRisk(tc.in); got != tc.want {
			t.Errorf("ParseBashRisk(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMaxBashRisk(t *testing.T) {
	if got := MaxBashRisk(BashRiskLow, BashRiskHigh); got != BashRiskHigh {
		t.Fatalf("MaxBashRisk(low, high) = %q", got)
	}
	if got := MaxBashRisk(BashRiskUnknown, BashRiskMedium); got != BashRiskMedium {
		t.Fatalf("MaxBashRisk(unknown, medium) = %q", got)
	}
}

func TestAssessBashRisk(t *testing.T) {
	fp := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	fp.SetResponses([][]provider.StreamEvent{
		provider.FakeTextResponse("high", &provider.Usage{InputTokens: 5, OutputTokens: 1}),
		provider.FakeTextResponse("high", &provider.Usage{InputTokens: 5, OutputTokens: 1}),
	})

	s := newTestStore(t)
	sid := newTestSession(t, s, "m")
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, nil, nil)
	a.SetModel("m")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	got := a.AssessBashRisk(ctx, "git push origin main", "push changes", "/tmp")
	if got != BashRiskHigh {
		t.Fatalf("AssessBashRisk = %q, want high", got)
	}
	if fp.CallCount() != bashRiskLLMRuns {
		t.Fatalf("CallCount = %d, want %d", fp.CallCount(), bashRiskLLMRuns)
	}
	var purpose string
	if err := s.DB().QueryRow(`SELECT purpose FROM api_calls WHERE session_id = ?`, sid).Scan(&purpose); err != nil {
		t.Fatalf("stored risk call: %v", err)
	}
	if purpose != "risk" {
		t.Fatalf("purpose = %q, want risk", purpose)
	}
	req := fp.LastRequest()
	if req == nil {
		t.Fatal("no risk request captured")
	}
	// The fake model isn't in KnownModels, so it has no configurable effort.
	// MaxTokens must be left unset (0) so an always-thinking model isn't starved
	// of room for the one-word verdict.
	if req.Effort != "" {
		t.Fatalf("risk request effort = %q, want empty (unknown model)", req.Effort)
	}
	if req.MaxTokens != 0 {
		t.Fatalf("expected MaxTokens 0 (headroom), got %d", req.MaxTokens)
	}
	if req.Temperature == nil || *req.Temperature != 0 {
		t.Fatalf("expected Temperature 0, got %+v", req.Temperature)
	}
}

// TestAssessBashRiskUsesLowestModelEffort verifies the classifier ignores the
// agent's configured effort and uses the model's LOWEST supported level, with
// the answer cap dropped so the (minimal) thinking has headroom.
func TestAssessBashRiskUsesLowestModelEffort(t *testing.T) {
	fp := provider.NewFakeProvider("anthropic", []provider.Model{{ID: "claude-opus-5", ContextWindow: 1000000}})
	fp.SetResponses([][]provider.StreamEvent{provider.FakeTextResponse("low", nil)})
	s := newTestStore(t)
	sid := newTestSession(t, s, "claude-opus-5")
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, nil, nil)
	a.SetModel("claude-opus-5")
	a.SetEffort("max") // agent runs heavy; the classifier must NOT inherit this

	a.AssessBashRisk(context.Background(), "git push origin main", "push", "/tmp")
	req := fp.LastRequest()
	if req == nil || req.Effort != "low" {
		t.Fatalf("expected lowest effort \"low\", got %+v", req)
	}
	if req.MaxTokens != 0 {
		t.Fatalf("expected MaxTokens 0 with effort, got %d", req.MaxTokens)
	}
}

func TestAssessBashRiskSingleRun(t *testing.T) {
	fp := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	fp.SetResponses([][]provider.StreamEvent{provider.FakeTextResponse("high", nil)})

	s := newTestStore(t)
	sid := newTestSession(t, s, "m")
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, nil, nil)
	a.SetModel("m")

	res := a.AssessBashRiskEval(context.Background(), "make deploy", "deploy", "/tmp", BashRiskEvalLLM)
	if res.Risk != BashRiskHigh {
		t.Fatalf("risk = %q, want high", res.Risk)
	}
	if len(res.LLMRuns) != 1 {
		t.Fatalf("LLMRuns = %d, want 1 (single round)", len(res.LLMRuns))
	}
	if fp.CallCount() != 1 {
		t.Fatalf("CallCount = %d, want 1", fp.CallCount())
	}
}

func TestAssessBashRiskThinkingOnly(t *testing.T) {
	fp := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	fp.SetResponses([][]provider.StreamEvent{
		{
			{Type: provider.EventThinkingDelta, Text: "This is destructive. high"},
			{Type: provider.EventDone, Usage: &provider.Usage{OutputTokens: 5}},
		},
		{
			{Type: provider.EventThinkingDelta, Text: "This is destructive. high"},
			{Type: provider.EventDone, Usage: &provider.Usage{OutputTokens: 5}},
		},
	})

	s := newTestStore(t)
	sid := newTestSession(t, s, "m")
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, nil, nil)
	a.SetModel("m")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	got := a.AssessBashRisk(ctx, "git push origin main", "push changes", "/tmp")
	if got != BashRiskHigh {
		t.Fatalf("AssessBashRisk = %q, want high from thinking fallback", got)
	}
}

func TestAssessBashRiskLLMFailureUnknown(t *testing.T) {
	fp := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	fp.SetResponses([][]provider.StreamEvent{
		provider.FakeErrorResponse(context.DeadlineExceeded),
		provider.FakeErrorResponse(context.DeadlineExceeded),
	})

	s := newTestStore(t)
	sid := newTestSession(t, s, "m")
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, nil, nil)
	a.SetModel("m")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// LLM failure must not fall back to the deterministic guard — it returns
	// unknown, which the approval gate routes to the human.
	got := a.AssessBashRisk(ctx, "cat /etc/passwd", "read passwd", "/tmp/proj")
	if got != BashRiskUnknown {
		t.Fatalf("AssessBashRisk(cat) on LLM failure = %q, want unknown", got)
	}
}

func TestGuardRiskFallback(t *testing.T) {
	if got := GuardRiskFallback("rmdir foo", ""); got != BashRiskHigh {
		t.Fatalf("GuardRiskFallback(rmdir) = %q, want high", got)
	}
	if got := GuardRiskFallback("make install", ""); got != BashRiskMedium {
		t.Fatalf("GuardRiskFallback(make) = %q, want medium", got)
	}
	if got := GuardRiskFallback("git status", ""); got != BashRiskLow {
		t.Fatalf("GuardRiskFallback(git status) = %q, want low", got)
	}
}

func TestAssessBashRiskEvalModes(t *testing.T) {
	fp := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	fp.SetResponses([][]provider.StreamEvent{
		provider.FakeTextResponse("low", nil),
		provider.FakeTextResponse("low", nil),
	})

	s := newTestStore(t)
	sid := newTestSession(t, s, "m")
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, nil, nil)
	a.SetModel("m")

	ctx := context.Background()
	llm := a.AssessBashRiskEval(ctx, "make install", "build", "/tmp", BashRiskEvalLLM)
	if llm.Risk != BashRiskLow || llm.Source != BashRiskSourceLLM {
		t.Fatalf("llm mode = %+v, want low/llm", llm)
	}

	guard := a.AssessBashRiskEval(ctx, "rmdir x", "", "/tmp", BashRiskEvalGuard)
	if guard.Risk != BashRiskHigh || guard.Source != BashRiskSourceGuard {
		t.Fatalf("guard mode = %+v, want high/guard", guard)
	}
}

func TestIsPackageInstallCommand(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		{"npm install", true},
		{"npm i", true},
		{"npm ci", true},
		{"npm add express", true},
		{"pnpm install", true},
		{"pnpm i", true},
		{"pnpm add lodash", true},
		{"yarn install", true},
		{"yarn add react", true},
		{"pip install requests", true},
		{"pip3 install flask", true},
		{"uv pip install httpx", true},
		{"uv add fastapi", true},
		{"go get github.com/foo/bar", true},
		{"go install github.com/foo/bar@latest", true},
		{"cargo add serde", true},
		{"cargo install ripgrep", true},
		{"apt install nginx", true},
		{"apt-get install curl", true},
		{"sudo apt install nginx", true},
		{"brew install jq", true},
		{"gem install rails", true},
		{"composer require monolog/monolog", true},
		{"composer install", true},
		{"poetry install", true},
		{"poetry add django", true},
		{"nix profile install nixpkgs#hello", true},
		{"pipx install ruff", true},
		// yarn/composer's "global" is a positional subcommand modifier —
		// the real verb sits one position further right.
		{"yarn global add evilpkg", true},
		{"composer global require vendor/evilpkg", true},
		// Chained commands.
		{"cd /tmp \u0026\u0026 npm install", true},
		{"echo hi; pip install evil", true},
		{"git pull \u0026\u0026 yarn install \u0026\u0026 yarn build", true},
		// Obfuscation — same rationale as TestIsDestructiveCommand above.
		{`\npm install evil-pkg`, true},
		{"NPM install evil-pkg", true},
		{"n''pm install evil-pkg", true},
		// Non-install commands.
		{"npm run build", false},
		{"npm test", false},
		{"pnpm list", false},
		{"yarn remove", false},
		{"pip show flask", false},
		{"go build", false},
		{"go test", false},
		{"cargo build", false},
		{"cargo run", false},
		{"make install", false}, // not a package manager
		{"git status", false},
		{"echo hello", false},
		{"ls -la", false},
		{"npm install -g", true},           // global install is still install
		{"pipx run ruff", false},           // run, not install (untrusted-exec instead)
		{"yarn global list", false},        // global read, not install
		{"composer global update", false},  // global mutation, but not require/install
		{"sh -c 'npm install evil'", true}, // shell-wrapped install
	}
	for _, c := range cases {
		got := isPackageInstallCommand(c.cmd)
		if got != c.want {
			t.Errorf("isPackageInstallCommand(%q) = %v, want %v", c.cmd, got, c.want)
		}
	}
}

// TestAssessBashRiskInstallFastPath verifies that install commands are
// escalated to medium without calling the LLM (zero provider calls).
func TestAssessBashRiskInstallFastPath(t *testing.T) {
	fp := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	fp.SetResponses([][]provider.StreamEvent{
		provider.FakeTextResponse("low", nil),
		provider.FakeTextResponse("low", nil),
	})
	s := newTestStore(t)
	sid := newTestSession(t, s, "m")
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, nil, nil)
	a.SetModel("m")

	got := a.AssessBashRisk(context.Background(), "npm install express", "install express", "/tmp")
	if got != BashRiskMedium {
		t.Fatalf("AssessBashRisk(npm install) = %q, want medium", got)
	}
	if fp.CallCount() != 0 {
		t.Fatalf("LLM was called %d times for an install command (should be 0)", fp.CallCount())
	}
}

func TestIsDestructiveCommand(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		{"rm file.txt", true},
		{"rm -rf node_modules", true},
		{"rm -rf /", true},
		{"sudo rm -rf /var/log", true},
		{"rmdir empty_dir", true},
		{"shred secret.txt", true},
		{"unlink symlink", true},
		{"truncate -s 0 log.txt", true},
		{"find . -delete", true},
		{"find . -name '*.tmp' -delete", true},
		{"find . -exec rm {} \\\\;", true},
		{"find . -execdir rmdir {} +", true},
		{"cd /tmp \u0026\u0026 rm -rf build", true},
		{"echo hi; rm foo", true},
		// Obfuscation guard.Classify itself already sees through — the
		// escalation must too, or a destructive command dressed up this way
		// skips straight to the (non-deterministic) LLM classifier instead
		// of a guaranteed BashRiskHigh. See TestWrapRiskGatedApproval_NeverAutoApprovesObfuscatedDestructiveCommand
		// for the full exploit-shaped end-to-end proof.
		{`\rm -rf /`, true},
		{`sudo \rm -rf /tmp/x`, true},
		{"RM -rf /tmp/x", true},
		{"/bin/rm -rf /tmp/x", true},
		{"Rmdir /tmp/empty", true},
		{"r''m -rf /tmp/x", true}, // quote-spliced — real bash for "rm"
		{"'r'm -rf /tmp/x", true},
		{`r"m" -rf /tmp/x`, true},
		// Red-team round 2: wrapper binaries beyond sudo/env/time/nohup/
		// command — everyday idioms for bounding/serializing/backgrounding
		// a command, not exotic obfuscation.
		{"timeout 10 rm -rf /tmp/foo", true},
		{"timeout -s SIGKILL 10 rm -rf /tmp/foo", true},
		{"nice -n 19 rm -rf /tmp/foo", true},
		{"flock /tmp/lock rm -rf /tmp/foo", true},
		{"flock -w 5 /tmp/lock rm -rf /tmp/foo", true},
		{"setsid rm -rf /tmp/foo", true},
		{"stdbuf -i0 rm -rf /tmp/foo", true},
		{"watch -n1 rm -rf /tmp/foo", true},
		{"busybox rm -rf /tmp/foo", true},
		{"sudo timeout 10 nice -n 19 rm -rf /tmp/foo", true}, // chained wrappers
		// xargs builds the wrapped command's argv from stdin — the
		// destructive verb is xargs's own argument, not its first token.
		{"find . -type f | xargs rm -f", true},
		{"ls -la | xargs rm -rf", true},
		{"find . -name '*.tmp' | xargs -I{} rm {}", true},
		// A subshell or brace group must be exactly as visible as its
		// unwrapped equivalent — including a later statement hidden behind
		// an innocuous first one (parens are depth-tracked, so a naive
		// split alone would never expose it).
		{"(rm -rf /tmp/foo)", true},
		{"{ rm -rf /tmp/foo; }", true},
		{"(echo hi; rm -rf /tmp/x)", true},
		{"(echo hi && rm -rf /tmp/x)", true},
		// A shell interpreter given a script string carries the real
		// command as an opaque argument, not a literal next token.
		{`find . -exec sh -c 'rm -rf {}' \;`, true},
		{`nohup sh -c 'rm -rf /tmp/foo' &`, true},
		{`bash -c "rm -rf /tmp/x"`, true},
		{`sh -c 'echo hi; rm -rf /tmp/x'`, true},
		// git subcommands that delete tracked files or discard work,
		// looked at past any wrapper prefix too.
		{"git rm -rf .", true},
		{"git rm file.go", true},
		{"git checkout -- .", true},
		{"git restore -- .", true},
		{"git reset --hard", true},
		{"git push --force", true},
		{"git push -f origin main", true},
		{"sudo git rm -rf .", true},
		// Non-destructive.
		{"cat file.txt", false},
		{"ls -la", false},
		{"git status", false},
		{"git push", false},          // plain push — LLM-judged, not a hard escalation
		{"git checkout main", false}, // branch switch, not a discard
		{"echo rm", false},
		{"find . -name '*.go'", false},
		{"find . -exec cat {} \\\\;", false},
		{"npm install", false}, // install, not destructive
		{"make install", false},
		{"nice -n 19 echo hi", false},
		{"timeout 10 npm test", false},
		{"sh -c 'echo hi'", false},
	}
	for _, c := range cases {
		got := isDestructiveCommand(c.cmd)
		if got != c.want {
			t.Errorf("isDestructiveCommand(%q) = %v, want %v", c.cmd, got, c.want)
		}
	}
}

// TestAssessBashRiskDestructiveFastPath verifies that rm commands are
// escalated to high without calling the LLM (zero provider calls).
func TestAssessBashRiskDestructiveFastPath(t *testing.T) {
	fp := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	fp.SetResponses([][]provider.StreamEvent{
		provider.FakeTextResponse("low", nil),
		provider.FakeTextResponse("low", nil),
	})
	s := newTestStore(t)
	sid := newTestSession(t, s, "m")
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, nil, nil)
	a.SetModel("m")

	got := a.AssessBashRisk(context.Background(), "rm -rf /tmp/build", "cleanup", "/tmp")
	if got != BashRiskHigh {
		t.Fatalf("AssessBashRisk(rm -rf) = %q, want high", got)
	}
	if fp.CallCount() != 0 {
		t.Fatalf("LLM was called %d times for a destructive command (should be 0)", fp.CallCount())
	}
}

// TestAssessBashRiskGitCommitFastPath verifies `git commit` is escalated to
// high without ever calling the LLM (zero provider calls) — even when the
// (fake) LLM is configured to say "low", proving the fast path short-
// circuits before any classification round trip, not just that it happens
// to agree with the LLM.
func TestAssessBashRiskGitCommitFastPath(t *testing.T) {
	fp := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	fp.SetResponses([][]provider.StreamEvent{
		provider.FakeTextResponse("low", nil),
		provider.FakeTextResponse("low", nil),
	})
	s := newTestStore(t)
	sid := newTestSession(t, s, "m")
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, nil, nil)
	a.SetModel("m")

	cases := []string{
		"git commit -m 'wip'",
		"git commit --amend",
		"cd /repo && git add -A && git commit -m done",
	}
	for _, cmd := range cases {
		got := a.AssessBashRisk(context.Background(), cmd, "commit changes", "/tmp")
		if got != BashRiskHigh {
			t.Errorf("AssessBashRisk(%q) = %q, want high", cmd, got)
		}
	}
	if fp.CallCount() != 0 {
		t.Fatalf("LLM was called %d times for git commit (should be 0 — must never be auto-approvable)", fp.CallCount())
	}
}

func TestIsUntrustedExecCommand(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		{"npx create-react-app my-app", true},
		{"npx cowsay", true},
		{"npx eslint .", true},
		{"sudo npx something", true},
		{"pnpm dlx cowsay", true},
		{"pnpx cowsay", true},
		{"yarn dlx prettier", true},
		{"pipx run ruff", true},
		{"bunx cowsay", true},
		{"dlx cowsay", true},
		{"cd /tmp \u0026\u0026 npx shadcn@latest add button", true},
		{"echo hi; npx evil", true},
		// Obfuscation — same rationale as TestIsDestructiveCommand above.
		{`\npx cowsay`, true},
		{"NPX cowsay", true},
		{"/usr/bin/npx cowsay", true},
		{"n''px cowsay", true},
		// Non-untrusted-exec.
		{"npm run build", false},
		{"pnpm install", false},
		{"yarn add react", false},
		{"pipx install ruff", false},  // install, not run
		{"pnpm exec eslint .", false}, // exec runs local binary
		{"git status", false},
		{"ls -la", false},
		{"cat file.txt", false},
	}
	for _, c := range cases {
		got := isUntrustedExecCommand(c.cmd)
		if got != c.want {
			t.Errorf("isUntrustedExecCommand(%q) = %v, want %v", c.cmd, got, c.want)
		}
	}
}

// TestAssessBashRiskUntrustedExecFastPath verifies that npx commands are
// escalated to high without calling the LLM (zero provider calls).
func TestAssessBashRiskUntrustedExecFastPath(t *testing.T) {
	fp := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	fp.SetResponses([][]provider.StreamEvent{
		provider.FakeTextResponse("low", nil),
		provider.FakeTextResponse("low", nil),
	})
	s := newTestStore(t)
	sid := newTestSession(t, s, "m")
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, nil, nil)
	a.SetModel("m")

	got := a.AssessBashRisk(context.Background(), "npx create-react-app", "scaffold app", "/tmp")
	if got != BashRiskHigh {
		t.Fatalf("AssessBashRisk(npx) = %q, want high", got)
	}
	if fp.CallCount() != 0 {
		t.Fatalf("LLM was called %d times for an untrusted-exec command (should be 0)", fp.CallCount())
	}
}
