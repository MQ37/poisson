package agent

import "github.com/mq37/poisson/internal/tools"

// humanApprovalNudge is appended to a tool_result's wire content when a
// human was actually asked to approve this call (see tools.ApprovalRecord)
// — so the model sees the interruption it caused and can weigh, on its own,
// whether the same operation is avoidable next time.
//
// Deliberately the same text whether approved or denied, and deliberately
// not naming the specific command: a denied call's own ToolError already
// carries the specific rejection reason, and a `batch` call's nested
// approval-gated steps all share one ApprovalRecord (see batch.go's
// mutatingTools) — wording that branched on outcome could describe the
// wrong outcome when a batch's gated steps disagree. "Something here needed
// a human" is true regardless of which step or which verdict won the race.
const humanApprovalNudge = "\n\n[This required human approval before running. " +
	"Prefer an approach that doesn't need manual approval when a safe " +
	"equivalent exists, so the user isn't interrupted unnecessarily. If this " +
	"action genuinely has no safer alternative — e.g. a real destructive or " +
	"critical host operation, a genuinely necessary sandbox mount — asking " +
	"was correct and no change is needed.]"

// humanApprovalNudgeFor returns humanApprovalNudge when rec shows a human
// was actually asked to approve this call, else "" — the common case, since
// the guard fast path and the LLM low-risk classifier both auto-approve
// without ever reaching a human.
func humanApprovalNudgeFor(rec *tools.ApprovalRecord) string {
	if rec == nil || !rec.Asked {
		return ""
	}
	return humanApprovalNudge
}
