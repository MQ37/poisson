package agent

import (
	"context"
	"testing"

	"poisson/internal/provider"
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
	approve := WrapRiskGatedApproval(a, func(_, _, _ string, _ BashRisk) bool {
		asked = true
		return false
	})

	if !approve(context.Background(), "gh run list --limit 5", "list runs", "/tmp") {
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
	approve := WrapRiskGatedApproval(a, func(_, _, _ string, risk BashRisk) bool {
		gotRisk = risk
		return true
	})

	if !approve(context.Background(), "rm -rf x", "delete", "/tmp") {
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
	approve := WrapRiskGatedApproval(a, func(_, _, _ string, risk BashRisk) bool {
		asked = true
		if risk == BashRiskLow {
			t.Fatalf("risk = low, want non-low fallback")
		}
		return false
	})

	_ = approve(context.Background(), "make install", "build", "/tmp")
	if !asked {
		t.Fatal("expected human prompt for non-low risk")
	}
}

func TestWrapRiskGatedApprovalNilAgent(t *testing.T) {
	asked := false
	approve := WrapRiskGatedApproval(nil, func(_, _, _ string, risk BashRisk) bool {
		asked = true
		if risk != BashRiskUnknown {
			t.Fatalf("risk = %q, want unknown", risk)
		}
		return true
	})
	if !approve(context.Background(), "rm -rf /", "x", "/") {
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
	approve := WrapRiskGatedApproval(a, func(_, _, _ string, risk BashRisk) bool {
		asked = true
		if risk != BashRiskMedium {
			t.Fatalf("risk = %q, want medium", risk)
		}
		return false
	})
	_ = approve(context.Background(), "npm install x", "add pkg", "/tmp")
	if !asked {
		t.Fatal("medium must not auto-allow")
	}
}

