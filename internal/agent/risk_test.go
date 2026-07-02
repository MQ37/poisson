package agent

import (
	"context"
	"testing"
	"time"

	"poisson/internal/config"
	"poisson/internal/provider"
)

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

	got := a.AssessBashRisk(ctx, "rm -rf /", "cleanup", "/tmp")
	if got != BashRiskHigh {
		t.Fatalf("AssessBashRisk = %q, want high", got)
	}
	if fp.CallCount() != bashRiskLLMRuns {
		t.Fatalf("CallCount = %d, want %d", fp.CallCount(), bashRiskLLMRuns)
	}
	req := fp.LastRequest()
	if req == nil {
		t.Fatal("no risk request captured")
	}
	// The default effort is propagated to the risk check, and the tiny token cap
	// is dropped so reasoning has headroom.
	if req.Effort != config.DefaultEffort {
		t.Fatalf("risk request effort = %q, want %q", req.Effort, config.DefaultEffort)
	}
	if req.MaxTokens != 0 {
		t.Fatalf("expected MaxTokens 0 with effort, got %d", req.MaxTokens)
	}
	if req.Temperature == nil || *req.Temperature != 0 {
		t.Fatalf("expected Temperature 0, got %+v", req.Temperature)
	}
}

// TestAssessBashRiskNoEffortCapsTokens verifies the fast path: with no reasoning
// effort the risk check keeps the tiny answer cap and sends no effort.
func TestAssessBashRiskNoEffortCapsTokens(t *testing.T) {
	fp := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	fp.SetResponses([][]provider.StreamEvent{
		provider.FakeTextResponse("low", nil),
		provider.FakeTextResponse("low", nil),
	})
	s := newTestStore(t)
	sid := newTestSession(t, s, "m")
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, nil, nil)
	a.SetModel("m")
	a.SetEffort("") // no reasoning → keep the tiny answer cap

	a.AssessBashRisk(context.Background(), "ls", "list", "/tmp")
	req := fp.LastRequest()
	if req == nil || req.MaxTokens != 32 || req.Effort != "" {
		t.Fatalf("expected MaxTokens 32 and empty effort, got %+v", req)
	}
}

func TestAssessBashRiskDualRunTakesHigher(t *testing.T) {
	fp := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	fp.SetResponses([][]provider.StreamEvent{
		provider.FakeTextResponse("low", nil),
		provider.FakeTextResponse("high", nil),
	})

	s := newTestStore(t)
	sid := newTestSession(t, s, "m")
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, nil, nil)
	a.SetModel("m")

	res := a.AssessBashRiskEval(context.Background(), "rm -rf /", "cleanup", "/tmp", BashRiskEvalLLM)
	if res.Risk != BashRiskHigh {
		t.Fatalf("dual run = %q, want high", res.Risk)
	}
	if len(res.LLMRuns) != 2 {
		t.Fatalf("LLMRuns = %d, want 2", len(res.LLMRuns))
	}
	if res.LLMRuns[0].Risk != BashRiskLow || res.LLMRuns[1].Risk != BashRiskHigh {
		t.Fatalf("runs = %+v", res.LLMRuns)
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

	got := a.AssessBashRisk(ctx, "rm -rf /", "cleanup", "/tmp")
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
	got := a.AssessBashRisk(ctx, "rmdir empty_dir", "remove empty directory", "/tmp/proj")
	if got != BashRiskUnknown {
		t.Fatalf("AssessBashRisk(rmdir) on LLM failure = %q, want unknown", got)
	}
}

func TestGuardRiskFallback(t *testing.T) {
	if got := GuardRiskFallback("rmdir foo"); got != BashRiskHigh {
		t.Fatalf("GuardRiskFallback(rmdir) = %q, want high", got)
	}
	if got := GuardRiskFallback("make install"); got != BashRiskMedium {
		t.Fatalf("GuardRiskFallback(make) = %q, want medium", got)
	}
	if got := GuardRiskFallback("git status"); got != BashRiskLow {
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
		// Chained commands.
		{"cd /tmp \u0026\u0026 npm install", true},
		{"echo hi; pip install evil", true},
		{"git pull \u0026\u0026 yarn install \u0026\u0026 yarn build", true},
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
		{"npm install -g", true}, // global install is still install
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