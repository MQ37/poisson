package agent

import "context"

// ApprovalOrigin identifies which part of poisson asked for a bash/file
// approval, so a human reviewing the prompt sees where the command actually
// came from — not every approval belongs to the visible main turn. /btw runs
// its own concurrent side question with its own overlay panel, and a
// subagent's approval is relayed from a child process; both need to be
// distinguishable from an ordinary main-conversation command, and /btw's
// additionally needs different overlay-coexistence handling (see
// tui.TUI.Approve) since it's already showing its own panel when the
// approval fires.
type ApprovalOrigin string

const (
	// ApprovalOriginMain is the default: an ordinary command from the main
	// conversation turn. Untagged contexts resolve to this — see
	// ApprovalOriginFromContext.
	ApprovalOriginMain ApprovalOrigin = "main"
	// ApprovalOriginBTW marks a command dispatched from a /btw side question.
	ApprovalOriginBTW ApprovalOrigin = "btw"
)

// SubagentOrigin formats the origin label for a named subagent's approval
// request, relayed from the child process (see tools.SubagentApproval) —
// this doesn't go through context tagging like the two constants above,
// since the subagent relay already carries the name as an explicit
// parameter on a completely separate call path.
func SubagentOrigin(agentName string) ApprovalOrigin {
	if agentName == "" {
		return ApprovalOrigin("subagent")
	}
	return ApprovalOrigin("subagent:" + agentName)
}

type approvalOriginCtxKey struct{}

// WithApprovalOrigin tags ctx with where a tool dispatch originated, read
// back by WrapRiskGatedApproval (and the file tools' own approval gate) when
// it needs to ask a human.
func WithApprovalOrigin(ctx context.Context, origin ApprovalOrigin) context.Context {
	return context.WithValue(ctx, approvalOriginCtxKey{}, origin)
}

// ApprovalOriginFromContext returns the tagged origin, or ApprovalOriginMain
// if none was set — the common case, since the main turn loop's own tool
// dispatch never tags its context.
func ApprovalOriginFromContext(ctx context.Context) ApprovalOrigin {
	if ctx != nil {
		if v, ok := ctx.Value(approvalOriginCtxKey{}).(ApprovalOrigin); ok {
			return v
		}
	}
	return ApprovalOriginMain
}
