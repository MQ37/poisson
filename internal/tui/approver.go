package tui

// Approver prompts the user to allow or deny a dangerous bash command.
// *TUI implements it.
type Approver interface {
	Approve(command, description string) bool
}