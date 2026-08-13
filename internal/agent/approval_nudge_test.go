package agent

import (
	"context"
	"testing"

	"github.com/mq37/poisson/internal/tools"
)

func TestHumanApprovalNudgeForNilRecord(t *testing.T) {
	if got := humanApprovalNudgeFor(nil); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestHumanApprovalNudgeForNeverAsked(t *testing.T) {
	_, rec := tools.WithApprovalRecord(context.Background())
	if got := humanApprovalNudgeFor(rec); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

// TestHumanApprovalNudgeForAskedSameEitherOutcome confirms the nudge text
// doesn't branch on Allowed — see humanApprovalNudge's doc comment for why
// (a denied call's ToolError already carries the reason; a batch's several
// gated steps share one ApprovalRecord, so a branching nudge could describe
// the wrong outcome).
func TestHumanApprovalNudgeForAskedSameEitherOutcome(t *testing.T) {
	ctx, rec := tools.WithApprovalRecord(context.Background())
	tools.RecordApproval(ctx, true)
	approved := humanApprovalNudgeFor(rec)
	if approved != humanApprovalNudge {
		t.Fatalf("got %q, want the constant nudge text", approved)
	}

	ctx2, rec2 := tools.WithApprovalRecord(context.Background())
	tools.RecordApproval(ctx2, false)
	denied := humanApprovalNudgeFor(rec2)
	if denied != approved {
		t.Fatalf("denied nudge %q != approved nudge %q, want identical text", denied, approved)
	}
}
