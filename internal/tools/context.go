package tools

import "context"

type toolCallIDKey struct{}

// WithToolCallID attaches the provider tool_call ID to ctx so a tool (e.g.
// SubagentTool) can correlate its own progress callbacks back to the specific
// running widget the TUI created for this call.
func WithToolCallID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, toolCallIDKey{}, id)
}

// ToolCallIDFromContext returns the ID attached by WithToolCallID, if any.
func ToolCallIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(toolCallIDKey{}).(string)
	return id, ok
}

// ApprovalRecord captures whether a live human was actually asked to approve
// this specific tool call, and what they decided. Every current and future
// approval path (bash's ApprovalFn, the sensitive-path FileApprovalFn,
// SandboxApprovalFn, and the subagent relay) funnels through the same single
// choke point — the interactive TUI's Approve method — so recording there
// covers any tool that gates on one of those hooks with no per-tool wiring.
// A nil record, or one whose Asked is false, means "never asked" — most
// calls, since the guard fast path and the LLM low-risk classifier both
// auto-approve without ever reaching a human; the caller renders no marker
// for those, matching intent (only mark commands a human actually decided).
type ApprovalRecord struct {
	Asked   bool
	Allowed bool
}

type approvalRecordKey struct{}

// WithApprovalRecord attaches a fresh *ApprovalRecord to ctx that RecordApproval
// can later fill in, and returns it so the caller can read it back once the
// tool call (which may or may not trigger an approval) has finished.
func WithApprovalRecord(ctx context.Context) (context.Context, *ApprovalRecord) {
	rec := &ApprovalRecord{}
	return context.WithValue(ctx, approvalRecordKey{}, rec), rec
}

// RecordApproval marks ctx's attached ApprovalRecord (if any) as asked, with
// the human's decision. A no-op when ctx carries no record — e.g. a /btw
// side question or a subagent-internal call, neither of which renders its
// own tool card, so there is nothing to mark.
func RecordApproval(ctx context.Context, allowed bool) {
	if rec, ok := ctx.Value(approvalRecordKey{}).(*ApprovalRecord); ok {
		rec.Asked = true
		rec.Allowed = allowed
	}
}

type approvalPauseKey struct{}

// ApprovalPause is a begin/end pair around wall-clock time that isn't real
// tool work — e.g. a bash call's pre-approval risk classification — so a
// timer implementation (the TUI's) can freeze elapsed-time displays for it,
// same as it already does for the human decision wait itself. Both fields
// are nil-safe to call directly.
type ApprovalPause struct {
	Begin func()
	End   func()
}

// WithApprovalPause attaches p to ctx so agent.WrapRiskGatedApproval (which
// cannot import the TUI package that implements timer freezing, to avoid an
// import cycle) can bracket its risk-classification call without knowing
// what's on the other end. Absent from ctx (headless/eval callers, tests) is
// the normal case and means "no timer to freeze" — every reader must treat
// that as a no-op, not an error.
func WithApprovalPause(ctx context.Context, p ApprovalPause) context.Context {
	return context.WithValue(ctx, approvalPauseKey{}, p)
}

// ApprovalPauseFromContext returns the ApprovalPause attached by
// WithApprovalPause, if any.
func ApprovalPauseFromContext(ctx context.Context) (ApprovalPause, bool) {
	p, ok := ctx.Value(approvalPauseKey{}).(ApprovalPause)
	return p, ok
}
