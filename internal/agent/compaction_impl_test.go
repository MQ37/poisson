package agent

import (
	"testing"

	"poisson/internal/store"
)

func TestAdjustCompactionCount_IncludesTrailingTools(t *testing.T) {
	msgs := []store.Message{
		{Seq: 1, Role: "user"},
		{Seq: 2, Role: "assistant"},
		{Seq: 3, Role: "tool"},
		{Seq: 4, Role: "tool"},
		{Seq: 5, Role: "assistant"},
	}
	got := adjustCompactionCount(msgs, 2)
	if got != 4 {
		t.Fatalf("adjustCompactionCount = %d, want 4 (include tool results)", got)
	}
}

func TestAdjustCompactionCount_NoOrphanTools(t *testing.T) {
	msgs := []store.Message{
		{Seq: 1, Role: "user"},
		{Seq: 2, Role: "assistant"},
		{Seq: 3, Role: "user"},
	}
	got := adjustCompactionCount(msgs, 2)
	if got != 2 {
		t.Fatalf("adjustCompactionCount = %d, want 2", got)
	}
}