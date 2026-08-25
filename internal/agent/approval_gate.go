package agent

import (
	"context"
	"time"

	"github.com/mq37/poisson/internal/guard"
	"github.com/mq37/poisson/internal/tools"
)

const approvalRiskTimeout = 45 * time.Second

// HumanApprovalFunc prompts the user after risk assessment when auto-allow
// does not apply. ctx carries the dispatch's tool-call correlation (see
// tools.WithApprovalRecord) so the implementation can report whether it
// actually asked a live human, for the conversation view's approved/denied
// marker — a no-op for any implementation that doesn't care. reason is an
// optional human-supplied explanation when denied. origin identifies where
// the command came from (main conversation, /btw, or a named subagent) so
// the prompt can say so — see ApprovalOrigin.
type HumanApprovalFunc func(ctx context.Context, command, description, workdir string, risk BashRisk, origin ApprovalOrigin) (allowed bool, reason string)

// WrapRiskGatedApproval returns an approval callback with two speeds,
// selected by a.ApprovalMode() (see ApprovalMode):
//
//   - Fast (default): a deterministic guard fast path (guard.ClassifyInDir)
//     auto-approves read-only, side-effect-free commands first, with no LLM
//     call and no human prompt at all. Anything the guard doesn't clear goes
//     to LLM risk classification, which auto-approves ONLY an LLM "low";
//     medium, high, and any failed or ambiguous classification (provider
//     error, timeout, unparseable output) fall through to the human. A
//     classifier "deny" (AssessBashRiskWithReason) is denied outright,
//     before the human is ever asked — see its own doc comment.
//   - Paranoid: both the guard fast path and the LLM classifier are skipped
//     entirely — every command asks the human, unconditionally. The
//     classifier never runs here, so a deny verdict never fires either;
//     Paranoid mode is the manual escape hatch if Fast mode's classifier
//     denies a command that's actually fine (false positive) — switching
//     modes puts the decision back in the human's hands.
//
// The guard fast path and the LLM path are independent auto-approve
// decisions; neither is ever consulted to silently allow what the other
// would reject — a failed LLM classification still falls through to the
// human exactly as before, guard or no guard.
func WrapRiskGatedApproval(a *Agent, ask HumanApprovalFunc) func(ctx context.Context, command, description, workdir string) (bool, string) {
	return func(ctx context.Context, command, description, workdir string) (bool, string) {
		origin := ApprovalOriginFromContext(ctx)
		if a != nil && a.ApprovalMode() == ApprovalModeParanoid {
			if ask == nil {
				return false, ""
			}
			return ask(ctx, command, description, workdir, BashRiskUnknown, origin)
		}
		if a != nil {
			if safe, _ := guard.ClassifyInDir(command, workdir); safe {
				return true, ""
			}
		}
		var risk BashRisk = BashRiskUnknown
		if a != nil {
			// This LLM call is real wall time but no more "the tool's own
			// work" than the human decision wait below is — for a sibling
			// bash call still queued behind this one (bash calls are
			// dispatched one at a time, in submission order, so siblings
			// spend this exact window idle, not executing), leaving it
			// unpaused inflates their displayed elapsed time by however
			// long classification took. Pause is a no-op when ctx carries
			// none (headless/eval callers).
			pause, hasPause := tools.ApprovalPauseFromContext(ctx)
			if hasPause && pause.Begin != nil {
				pause.Begin()
			}
			rctx, cancel := context.WithTimeout(ctx, approvalRiskTimeout)
			var deny bool
			var denyReason string
			risk, deny, denyReason = a.AssessBashRiskWithReason(rctx, command, description, workdir)
			cancel()
			if hasPause && pause.End != nil {
				pause.End()
			}
			if deny {
				return false, secretLeakDenyMessage(denyReason)
			}
			if risk == BashRiskLow {
				return true, ""
			}
		}
		if ask == nil {
			return false, ""
		}
		return ask(ctx, command, description, workdir, risk, origin)
	}
}

// secretLeakDenyMessage builds the reason text for a classifier deny —
// forwarded to the model as the bash tool's error (see BashTool.Execute),
// same as any other rejection, so it understands *why*, not just that it
// was rejected: the classifier's own stated reason, plus how to get a human
// to review it if this looks like a false positive.
func secretLeakDenyMessage(reason string) string {
	if reason == "" {
		reason = "its own output would likely print a secret or credential value directly"
	}
	return "risk classifier auto-denied this command: " + reason +
		". This is unconditional in Fast mode — no human was asked. " +
		"If this is a false positive, switch to Paranoid mode (Shift+Tab) and retry; a human can approve it there."
}
