package tui

import "poisson/internal/agent"

// Approver prompts the user to allow or deny a dangerous bash command.
// risk is precomputed by the risk gate; BashRiskUnknown means assess in the background.
type Approver interface {
	Approve(command, description, workdir string, risk agent.BashRisk) bool
}