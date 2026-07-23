package agent

import (
	"context"
	"time"

	"github.com/mq37/poisson/internal/guard"
)

const approvalRiskTimeout = 45 * time.Second

// HumanApprovalFunc prompts the user after risk assessment when auto-allow
// does not apply. reason is an optional human-supplied explanation when
// denied. origin identifies where the command came from (main conversation,
// /btw, or a named subagent) so the prompt can say so — see ApprovalOrigin.
type HumanApprovalFunc func(command, description, workdir string, risk BashRisk, origin ApprovalOrigin) (allowed bool, reason string)

// WrapRiskGatedApproval returns an approval callback with two speeds,
// selected by a.ApprovalMode() (see ApprovalMode):
//
//   - Fast (default): a deterministic guard fast path (guard.ClassifyInDir)
//     auto-approves read-only, side-effect-free commands first, with no LLM
//     call and no human prompt at all. Anything the guard doesn't clear goes
//     to LLM risk classification, which auto-approves ONLY an LLM "low";
//     medium, high, and any failed or ambiguous classification (provider
//     error, timeout, unparseable output) fall through to the human.
//   - Paranoid: both the guard fast path and the LLM classifier are skipped
//     entirely — every command asks the human, unconditionally.
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
			return ask(command, description, workdir, BashRiskUnknown, origin)
		}
		if a != nil {
			if safe, _ := guard.ClassifyInDir(command, workdir); safe {
				return true, ""
			}
		}
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
		return ask(command, description, workdir, risk, origin)
	}
}
