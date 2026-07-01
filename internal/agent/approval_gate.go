package agent

import (
	"context"
	"time"
)

const approvalRiskTimeout = 45 * time.Second

// HumanApprovalFunc prompts the user after risk assessment when auto-allow does not apply.
type HumanApprovalFunc func(command, description, workdir string, risk BashRisk) bool

// WrapRiskGatedApproval returns an approval callback that runs risk assessment first.
// Only BashRiskLow is auto-allowed; medium, high, and unknown require HumanApprovalFunc.
func WrapRiskGatedApproval(a *Agent, ask HumanApprovalFunc) func(command, description, workdir string) bool {
	return func(command, description, workdir string) bool {
		var risk BashRisk = BashRiskUnknown
		if a != nil {
			ctx, cancel := context.WithTimeout(context.Background(), approvalRiskTimeout)
			risk = a.AssessBashRisk(ctx, command, description, workdir)
			cancel()
			if risk == BashRiskLow {
				return true
			}
		}
		if ask == nil {
			return false
		}
		return ask(command, description, workdir, risk)
	}
}