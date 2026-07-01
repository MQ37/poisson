package agent

import (
	"context"
	"testing"
	"time"

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
	if req == nil || req.MaxTokens != 32 {
		t.Fatalf("expected MaxTokens 32 risk request, got %+v", req)
	}
	if req.Temperature == nil || *req.Temperature != 0 {
		t.Fatalf("expected Temperature 0, got %+v", req.Temperature)
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

func TestAssessBashRiskGuardFallbackRmdir(t *testing.T) {
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

	got := a.AssessBashRisk(ctx, "rmdir empty_dir", "remove empty directory", "/tmp/proj")
	if got != BashRiskHigh {
		t.Fatalf("AssessBashRisk(rmdir) = %q, want high from guard fallback", got)
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