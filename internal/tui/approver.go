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

// SudoPasswordAsker prompts for the sudo password a host bash command needs
// (see guard.RequiresSudoPassword and tools.SudoPasswordFn), always after
// that command's own Approve above already granted it. ok is false on
// cancel. password must never be logged or handed to the model — the only
// caller (BashTool's host exec path) writes it straight into a private
// one-shot askpass file and zeroes its own copy right after.
type SudoPasswordAsker interface {
	AskSudoPassword(ctx context.Context, command, description, workdir string) (password []byte, ok bool)
}
