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
