package agent

// ApprovalMode selects how much of the bash approval gate runs automatically.
// Toggled at runtime by the TUI (Shift+Tab) and shown in the status bar.
type ApprovalMode int32

const (
	// ApprovalModeFast is the default: a deterministic guard fast path
	// auto-approves read-only, side-effect-free commands with no LLM call at
	// all, everything else goes through LLM risk classification, and only
	// medium/high/unknown falls through to a human. Zero is Fast so a
	// freshly constructed Agent (zero-value approvalMode) starts here.
	ApprovalModeFast ApprovalMode = iota
	// ApprovalModeParanoid disables both the guard fast path and the LLM
	// classifier — every bash command, no matter how trivial, asks a human.
	ApprovalModeParanoid
)

func (m ApprovalMode) String() string {
	if m == ApprovalModeParanoid {
		return "paranoid"
	}
	return "fast"
}

// ApprovalMode returns the agent's current approval mode.
func (a *Agent) ApprovalMode() ApprovalMode {
	if a == nil {
		return ApprovalModeFast
	}
	return ApprovalMode(a.approvalMode.Load())
}

// SetApprovalMode changes the agent's approval mode. Safe to call from any
// goroutine (e.g. the TUI's input goroutine while a turn is running).
func (a *Agent) SetApprovalMode(m ApprovalMode) {
	if a == nil {
		return
	}
	a.approvalMode.Store(int32(m))
}
