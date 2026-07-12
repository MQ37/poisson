package agent

import (
	"context"
	"time"
)

const approvalRiskTimeout = 45 * time.Second

// HumanApprovalFunc prompts the user after risk assessment when auto-allow
// does not apply. reason is an optional human-supplied explanation when denied.
type HumanApprovalFunc func(command, description, workdir string, risk BashRisk) (allowed bool, reason string)

// WrapRiskGatedApproval returns an approval callback that classifies the
// command with the LLM. It auto-approves ONLY when the LLM itself returned
// "low"; medium, high, and any failed or ambiguous classification (provider
// error, timeout, unparseable output) fall through to the human. The
// deterministic guard is never consulted for the auto-approve decision, so a
// failed classification can never silently allow a command.
func WrapRiskGatedApproval(a *Agent, ask HumanApprovalFunc) func(ctx context.Context, command, description, workdir string) (bool, string) {
	return func(ctx context.Context, command, description, workdir string) (bool, string) {
		var risk BashRisk = BashRiskUnknown
		if a != nil {
			rctx, cancel := context.WithTimeout(ctx, approvalRiskTimeout)
			risk = a.AssessBashRisk(rctx, command, description, workdir)
			cancel()
			if risk == BashRiskLow {
				return true, ""
			}
		}
		if ask == nil {
			return false, ""
		}
		return ask(command, description, workdir, risk)
	}
}
