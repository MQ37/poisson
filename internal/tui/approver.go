package tui

// Approver prompts the user to allow or deny a dangerous bash command.
// Both *TUI (classic) and *tuiV2 implement it.
type Approver interface {
	Approve(command, description string) bool
}