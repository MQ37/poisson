package tools

import (
	"context"
	"testing"
)

func TestToolCallIDContextRoundTrip(t *testing.T) {
	ctx := WithToolCallID(context.Background(), "toolu_123")
	id, ok := ToolCallIDFromContext(ctx)
	if !ok || id != "toolu_123" {
		t.Fatalf("got (%q, %v), want (toolu_123, true)", id, ok)
	}
}

func TestToolCallIDContextMissing(t *testing.T) {
	_, ok := ToolCallIDFromContext(context.Background())
	if ok {
		t.Fatal("expected ok=false for a context with no tool call ID")
	}
}

func TestApprovalRecordRoundTripApproved(t *testing.T) {
	ctx, rec := WithApprovalRecord(context.Background())
	RecordApproval(ctx, true)
	if !rec.Asked || !rec.Allowed {
		t.Fatalf("rec = %+v, want {Asked:true Allowed:true}", rec)
	}
}

func TestApprovalRecordRoundTripDenied(t *testing.T) {
	ctx, rec := WithApprovalRecord(context.Background())
	RecordApproval(ctx, false)
	if !rec.Asked || rec.Allowed {
		t.Fatalf("rec = %+v, want {Asked:true Allowed:false}", rec)
	}
}

// TestApprovalRecordNeverAsked is the "auto-approved, no human involved"
// case: the vast majority of calls (guard fast path or LLM low-risk) never
// touch RecordApproval at all, so the attached record must stay unasked —
// the caller's signal to render no marker.
func TestApprovalRecordNeverAsked(t *testing.T) {
	_, rec := WithApprovalRecord(context.Background())
	if rec.Asked {
		t.Fatal("expected Asked=false when RecordApproval was never called")
	}
}

// TestRecordApprovalNoRecordAttached verifies RecordApproval is a safe no-op
// against a plain context — the /btw and subagent-relay case, neither of
// which attaches a record since neither has its own tool card to mark.
func TestRecordApprovalNoRecordAttached(t *testing.T) {
	RecordApproval(context.Background(), true) // must not panic
}
