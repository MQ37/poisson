package agent

import (
	"context"
	"testing"

	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/tools"
)

func TestWrapRiskGatedApprovalAutoLow(t *testing.T) {
	fp := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	fp.SetResponses([][]provider.StreamEvent{
		provider.FakeTextResponse("low", nil),
		provider.FakeTextResponse("low", nil),
	})

	s := newTestStore(t)
	sid := newTestSession(t, s, "m")
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, nil, nil)
	a.SetModel("m")

	asked := false
	approve := WrapRiskGatedApproval(a, func(_ context.Context, _, _, _ string, _ BashRisk, _ ApprovalOrigin) (bool, string) {
		asked = true
		return false, ""
	})

	allowed, _ := approve(context.Background(), "gh run list --limit 5", "list runs", "/tmp")
	if !allowed {
		t.Fatal("expected auto-allow for low risk")
	}
	if asked {
		t.Fatal("human approval should not run for low risk")
	}
}

func TestWrapRiskGatedApprovalRequiresHumanForHigh(t *testing.T) {
	fp := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	fp.SetResponses([][]provider.StreamEvent{
		provider.FakeTextResponse("high", nil),
		provider.FakeTextResponse("high", nil),
	})

	s := newTestStore(t)
	sid := newTestSession(t, s, "m")
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, nil, nil)
	a.SetModel("m")

	var gotRisk BashRisk
	approve := WrapRiskGatedApproval(a, func(_ context.Context, _, _, _ string, risk BashRisk, _ ApprovalOrigin) (bool, string) {
		gotRisk = risk
		return true, ""
	})

	allowed, _ := approve(context.Background(), "rm -rf x", "delete", "/tmp")
	if !allowed {
		t.Fatal("expected human allow")
	}
	if gotRisk != BashRiskHigh {
		t.Fatalf("risk = %q, want high", gotRisk)
	}
}

func TestWrapRiskGatedApprovalRequiresHumanWhenLLMFails(t *testing.T) {
	fp := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	fp.SetResponses([][]provider.StreamEvent{
		provider.FakeErrorResponse(context.DeadlineExceeded),
		provider.FakeErrorResponse(context.DeadlineExceeded),
	})

	s := newTestStore(t)
	sid := newTestSession(t, s, "m")
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, nil, nil)
	a.SetModel("m")

	asked := false
	approve := WrapRiskGatedApproval(a, func(_ context.Context, _, _, _ string, risk BashRisk, _ ApprovalOrigin) (bool, string) {
		asked = true
		if risk == BashRiskLow {
			t.Fatalf("risk = low, want non-low fallback")
		}
		return false, ""
	})

	_, _ = approve(context.Background(), "make install", "build", "/tmp")
	if !asked {
		t.Fatal("expected human prompt for non-low risk")
	}
}

// TestWrapRiskGatedApprovalRequiresHumanForGitCommit verifies `git commit`
// always reaches the human, even with a FakeProvider that would say "low" if
// consulted — proving the gate never gives the LLM a chance to auto-approve
// a commit at all.
func TestWrapRiskGatedApprovalRequiresHumanForGitCommit(t *testing.T) {
	fp := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	fp.SetResponses([][]provider.StreamEvent{
		provider.FakeTextResponse("low", nil),
		provider.FakeTextResponse("low", nil),
	})

	s := newTestStore(t)
	sid := newTestSession(t, s, "m")
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, nil, nil)
	a.SetModel("m")

	var gotRisk BashRisk
	asked := false
	approve := WrapRiskGatedApproval(a, func(_ context.Context, _, _, _ string, risk BashRisk, _ ApprovalOrigin) (bool, string) {
		asked = true
		gotRisk = risk
		return true, ""
	})

	allowed, _ := approve(context.Background(), "git commit -m 'wip'", "commit changes", "/tmp")
	if !allowed {
		t.Fatal("expected human allow")
	}
	if !asked {
		t.Fatal("git commit must always reach the human, never auto-approve")
	}
	if gotRisk != BashRiskHigh {
		t.Fatalf("risk = %q, want high", gotRisk)
	}
	if fp.CallCount() != 0 {
		t.Fatalf("LLM was called %d times for git commit (should be 0)", fp.CallCount())
	}
}

func TestWrapRiskGatedApprovalNilAgent(t *testing.T) {
	asked := false
	approve := WrapRiskGatedApproval(nil, func(_ context.Context, _, _, _ string, risk BashRisk, _ ApprovalOrigin) (bool, string) {
		asked = true
		if risk != BashRiskUnknown {
			t.Fatalf("risk = %q, want unknown", risk)
		}
		return true, ""
	})
	allowed, _ := approve(context.Background(), "rm -rf /", "x", "/")
	if !allowed {
		t.Fatal("expected ask to allow")
	}
	if !asked {
		t.Fatal("expected human approval when agent nil")
	}
}

func TestWrapRiskGatedApprovalMediumNotAuto(t *testing.T) {
	fp := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	fp.SetResponses([][]provider.StreamEvent{
		provider.FakeTextResponse("medium", nil),
		provider.FakeTextResponse("medium", nil),
	})

	s := newTestStore(t)
	sid := newTestSession(t, s, "m")
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, nil, nil)
	a.SetModel("m")

	asked := false
	approve := WrapRiskGatedApproval(a, func(_ context.Context, _, _, _ string, risk BashRisk, _ ApprovalOrigin) (bool, string) {
		asked = true
		if risk != BashRiskMedium {
			t.Fatalf("risk = %q, want medium", risk)
		}
		return false, ""
	})
	_, _ = approve(context.Background(), "npm install x", "add pkg", "/tmp")
	if !asked {
		t.Fatal("medium must not auto-allow")
	}
}

// TestWrapRiskGatedApprovalForwardsReason verifies a human's denial reason
// passes straight through the risk gate unmodified.
func TestWrapRiskGatedApprovalForwardsReason(t *testing.T) {
	approve := WrapRiskGatedApproval(nil, func(_ context.Context, _, _, _ string, _ BashRisk, _ ApprovalOrigin) (bool, string) {
		return false, "not now, finish the tests first"
	})
	allowed, reason := approve(context.Background(), "rm -rf /", "x", "/")
	if allowed {
		t.Fatal("expected deny")
	}
	if reason != "not now, finish the tests first" {
		t.Fatalf("reason = %q, want it forwarded unmodified", reason)
	}
}

// TestWrapRiskGatedApprovalFastModeGuardAutoApprove verifies the guard fast
// path auto-approves a read-only command with ZERO LLM calls and no human
// prompt — the whole point of Fast mode.
func TestWrapRiskGatedApprovalFastModeGuardAutoApprove(t *testing.T) {
	fp := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	// No responses queued: any provider call would panic/fail on FakeProvider,
	// proving the guard fast path never reaches the LLM.
	s := newTestStore(t)
	sid := newTestSession(t, s, "m")
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, nil, nil)
	a.SetModel("m")

	asked := false
	approve := WrapRiskGatedApproval(a, func(_ context.Context, _, _, _ string, _ BashRisk, _ ApprovalOrigin) (bool, string) {
		asked = true
		return false, ""
	})
	allowed, _ := approve(context.Background(), "ls -la", "list files", "/tmp")
	if !allowed {
		t.Fatal("expected guard fast path to auto-approve a safe command")
	}
	if asked {
		t.Fatal("human approval should not run for a guard-safe command")
	}
	if fp.CallCount() != 0 {
		t.Fatalf("LLM was called %d times, want 0 (guard fast path should short-circuit)", fp.CallCount())
	}
}

// TestWrapRiskGatedApprovalParanoidModeAsksAlways verifies Paranoid mode
// skips BOTH the guard fast path and the LLM classifier for a trivially safe
// command — every command reaches the human.
func TestWrapRiskGatedApprovalParanoidModeAsksAlways(t *testing.T) {
	fp := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	s := newTestStore(t)
	sid := newTestSession(t, s, "m")
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, nil, nil)
	a.SetModel("m")
	a.SetApprovalMode(ApprovalModeParanoid)

	asked := false
	var gotRisk BashRisk
	approve := WrapRiskGatedApproval(a, func(_ context.Context, _, _, _ string, risk BashRisk, _ ApprovalOrigin) (bool, string) {
		asked = true
		gotRisk = risk
		return true, ""
	})
	allowed, _ := approve(context.Background(), "echo hi", "say hi", "/tmp")
	if !allowed {
		t.Fatal("expected human allow")
	}
	if !asked {
		t.Fatal("paranoid mode must always ask the human, even for a trivial command")
	}
	if gotRisk != BashRiskUnknown {
		t.Fatalf("risk = %q, want unknown (no classification in paranoid mode)", gotRisk)
	}
	if fp.CallCount() != 0 {
		t.Fatalf("LLM was called %d times, want 0 (paranoid mode must not classify)", fp.CallCount())
	}
}

// TestWrapRiskGatedApprovalPausesTimerDuringClassification verifies the risk
// classification LLM call is bracketed by ctx's tools.ApprovalPause hook —
// so a sibling bash call still queued behind this one (they run one at a
// time; see agent.go's gated walker) doesn't have this call's classification
// latency silently added to its own displayed elapsed time. Regression test
// for that exact bug: previously only the human decision wait (TUI.Approve)
// was covered, leaving this earlier window unpaused.
func TestWrapRiskGatedApprovalPausesTimerDuringClassification(t *testing.T) {
	fp := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	fp.SetResponses([][]provider.StreamEvent{
		provider.FakeTextResponse("medium", nil),
		provider.FakeTextResponse("medium", nil),
	})

	s := newTestStore(t)
	sid := newTestSession(t, s, "m")
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, nil, nil)
	a.SetModel("m")

	var begins, ends int
	ctx := tools.WithApprovalPause(context.Background(), tools.ApprovalPause{
		Begin: func() { begins++ },
		End:   func() { ends++ },
	})

	approve := WrapRiskGatedApproval(a, func(_ context.Context, _, _, _ string, _ BashRisk, _ ApprovalOrigin) (bool, string) {
		// Classification's own pause window (begin/end around
		// AssessBashRisk) is already closed by the time ask() runs — the
		// separate human-decision-wait window is TUI.Approve's to open,
		// not this call's.
		if begins != 1 || ends != 1 {
			t.Fatalf("entering human ask: begins=%d ends=%d, want 1,1", begins, ends)
		}
		return true, ""
	})

	if _, _ = approve(ctx, "npm install x", "add pkg", "/tmp"); begins != 1 || ends != 1 {
		t.Fatalf("begins=%d ends=%d, want 1,1", begins, ends)
	}
}

// TestWrapRiskGatedApprovalNoPauseHookIsNoop verifies a ctx with no
// tools.ApprovalPause attached (headless callers, every existing test above)
// works exactly as before — the pause lookup must never panic or require
// one.
func TestWrapRiskGatedApprovalNoPauseHookIsNoop(t *testing.T) {
	fp := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	fp.SetResponses([][]provider.StreamEvent{
		provider.FakeTextResponse("low", nil),
		provider.FakeTextResponse("low", nil),
	})
	s := newTestStore(t)
	sid := newTestSession(t, s, "m")
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, nil, nil)
	a.SetModel("m")

	approve := WrapRiskGatedApproval(a, func(_ context.Context, _, _, _ string, _ BashRisk, _ ApprovalOrigin) (bool, string) {
		return false, ""
	})
	if allowed, _ := approve(context.Background(), "gh run list", "list", "/tmp"); !allowed {
		t.Fatal("expected auto-allow for low risk")
	}
}

// TestApprovalModeDefaultIsFast verifies a freshly constructed Agent starts
// in Fast mode (zero value), and Shift+Tab-style toggling flips it both ways.
func TestApprovalModeDefaultIsFast(t *testing.T) {
	s := newTestStore(t)
	sid := newTestSession(t, s, "m")
	a := NewAgent(s, newFakeProvider(), newTestRegistry("."), newTestConfig(), sid, nil, nil)
	if a.ApprovalMode() != ApprovalModeFast {
		t.Fatalf("default ApprovalMode = %v, want Fast", a.ApprovalMode())
	}
	a.SetApprovalMode(ApprovalModeParanoid)
	if a.ApprovalMode() != ApprovalModeParanoid {
		t.Fatal("SetApprovalMode(Paranoid) did not take")
	}
	a.SetApprovalMode(ApprovalModeFast)
	if a.ApprovalMode() != ApprovalModeFast {
		t.Fatal("SetApprovalMode(Fast) did not take")
	}
}
