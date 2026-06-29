package tui

// Approver prompts the user to allow or deny a dangerous bash command.
type Approver interface {
	Approve(command, description, workdir string) bool
}