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
