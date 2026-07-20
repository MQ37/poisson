package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/store"
)

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
	a.AssessBashRisk(ctx, "git commit -am wip", "commit work", "/tmp")

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
	if !strings.Contains(userText, "git commit -am wip") {
		t.Errorf("risk prompt should contain the command, got %q", userText)
	}
	// The conversation must not appear anywhere in the request.
	if strings.Contains(userText, marker) || strings.Contains(req.System[0].Text, marker) {
		t.Error("conversation history leaked into the bash-risk request")
	}
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
	fp := provider.NewFakeProvider("anthropic", []provider.Model{{ID: "claude-opus-4-8", ContextWindow: 1000000}})
	fp.SetResponses([][]provider.StreamEvent{provider.FakeTextResponse("low", nil)})
	s := newTestStore(t)
	sid := newTestSession(t, s, "claude-opus-4-8")
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, nil, nil)
	a.SetModel("claude-opus-4-8")
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
		// Non-destructive.
		{"cat file.txt", false},
		{"ls -la", false},
		{"git status", false},
		{"echo rm", false},
		{"find . -name '*.go'", false},
		{"find . -exec cat {} \\\\;", false},
		{"npm install", false}, // install, not destructive
		{"make install", false},
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
