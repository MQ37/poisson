package tui

import "github.com/mq37/poisson/internal/agent"

// Approver prompts the user to allow or deny a dangerous bash command.
// risk is precomputed by the risk gate; BashRiskUnknown means assess in the
// background. reason is an optional human-supplied explanation when denied.
type Approver interface {
	Approve(command, description, workdir string, risk agent.BashRisk) (allowed bool, reason string)
}
