package agent

import (
	"context"
	"testing"

	"github.com/mq37/poisson/internal/provider"
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
	approve := WrapRiskGatedApproval(a, func(_, _, _ string, _ BashRisk) (bool, string) {
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
	approve := WrapRiskGatedApproval(a, func(_, _, _ string, risk BashRisk) (bool, string) {
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
	approve := WrapRiskGatedApproval(a, func(_, _, _ string, risk BashRisk) (bool, string) {
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
	approve := WrapRiskGatedApproval(a, func(_, _, _ string, risk BashRisk) (bool, string) {
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
	approve := WrapRiskGatedApproval(nil, func(_, _, _ string, risk BashRisk) (bool, string) {
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
	approve := WrapRiskGatedApproval(a, func(_, _, _ string, risk BashRisk) (bool, string) {
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
	approve := WrapRiskGatedApproval(nil, func(_, _, _ string, _ BashRisk) (bool, string) {
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
