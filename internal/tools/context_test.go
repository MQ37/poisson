package tools

import (
	"context"
	"testing"
)

func TestToolCallIDContextRoundTrip(t *testing.T) {
	ctx := WithToolCallID(context.Background(), "toolu_123")
	id, ok := ToolCallIDFromContext(ctx)
	if !ok || id != "toolu_123" {
		t.Fatalf("got (%q, %v), want (toolu_123, true)", id, ok)
	}
}

func TestToolCallIDContextMissing(t *testing.T) {
	_, ok := ToolCallIDFromContext(context.Background())
	if ok {
		t.Fatal("expected ok=false for a context with no tool call ID")
	}
}
