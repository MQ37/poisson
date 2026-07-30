package tui

import (
	"context"

	"github.com/mq37/poisson/internal/agent"
)

// Approver prompts the user to allow or deny a dangerous bash command.
// ctx carries the tool call's correlation (see tools.WithApprovalRecord) so
// the implementation can report the decision back for the conversation
// view's approved/denied marker. risk is precomputed by the risk gate;
// BashRiskUnknown means assess in the background. reason is an optional
// human-supplied explanation when denied. origin identifies where the
// command came from (main conversation, /btw, or a named subagent) — see
// agent.ApprovalOrigin.
type Approver interface {
	Approve(ctx context.Context, command, description, workdir string, risk agent.BashRisk, origin agent.ApprovalOrigin) (allowed bool, reason string)
}
