package tui

import (
	"strings"
	"testing"
)

const subagentResultWithCost = "Explored the flow.\n\n---\nSubagent finished. 4 tool calls, 3 turns. Cost: $0.0071."

const subagentResultWithRanOn = "Explored the flow.\n\n---\nSubagent finished. 4 tool calls, 3 turns. Cost: $0.0071. Ran on anthropic/claude-opus-5 (xhigh effort)."

func TestSubagentRanOnFromResult_ParsesModelAndEffort(t *testing.T) {
	label, ok := subagentRanOnFromResult(subagentResultWithRanOn)
	if !ok || label != "xhigh · anthropic/claude-opus-5" {
		t.Fatalf("label=%q ok=%v, want %q/true", label, ok, "xhigh · anthropic/claude-opus-5")
	}
}

func TestSubagentRanOnFromResult_NoEffortSuffix(t *testing.T) {
	const content = "done.\n\n---\nSubagent finished. 1 tool calls, 1 turns. Ran on xai/grok-build."
	label, ok := subagentRanOnFromResult(content)
	if !ok || label != "xai/grok-build" {
		t.Fatalf("label=%q ok=%v, want %q/true", label, ok, "xai/grok-build")
	}
}

func TestSubagentRanOnFromResult_AbsentOnOldSession(t *testing.T) {
	// A result recorded before this feature existed (or one cancelled
	// before reaching the summary line) has no "Ran on" marker at all.
	const content = "partial output\n\n---\nSubagent finished. 1 tool calls, 1 turns."
	if _, ok := subagentRanOnFromResult(content); ok {
		t.Fatal("want ok=false when the result text has no Ran-on line")
	}
}

// TestCompleteSubagentCard_RanOnOverridesModelSwitchedMidSession is the
// actual bug this fixes: the widget is appended with whatever the main
// session's model happened to be AT SPAWN TIME (here "claude-sonnet-5" —
// simulating the append-time guess), but by completion time the user has
// switched the main session to a different model. Without the fix, a
// resumed/re-rendered widget would silently show whatever the CURRENT main
// model is instead of what this specific subagent call actually ran on.
func TestCompleteSubagentCard_RanOnOverridesModelSwitchedMidSession(t *testing.T) {
	s := newScrollback(200)
	s.appendSubagentCard(1, "call-1", "explore", "Explore the flow", "claude-sonnet-5") // append-time guess

	if !s.completeSubagentCard("call-1", subagentResultWithRanOn, "", 1000) {
		t.Fatal("completeSubagentCard did not match call-1")
	}
	var card *Block
	for i := range s.blocks {
		if s.blocks[i].kind == blockSubagent {
			card = &s.blocks[i]
		}
	}
	if card == nil {
		t.Fatal("no subagent card found")
	}
	if card.meta.SubagentModel != "xhigh · anthropic/claude-opus-5" {
		t.Fatalf("SubagentModel = %q, want the authoritative Ran-on value to have overwritten the append-time guess", card.meta.SubagentModel)
	}
}

// TestCompleteSubagentCard_KeepsGuessWhenNoRanOnMarker covers an old
// session's tool_result predating this feature: there's no "Ran on" line to
// read, so the append-time guess must survive untouched rather than being
// clobbered with an empty string.
func TestCompleteSubagentCard_KeepsGuessWhenNoRanOnMarker(t *testing.T) {
	s := newScrollback(200)
	s.appendSubagentCard(1, "call-1", "explore", "Explore the flow", "anthropic/claude-sonnet-5")

	const oldContent = "Explored the flow.\n\n---\nSubagent finished. 4 tool calls, 3 turns."
	if !s.completeSubagentCard("call-1", oldContent, "", 1000) {
		t.Fatal("completeSubagentCard did not match call-1")
	}
	if s.blocks[0].meta.SubagentModel != "anthropic/claude-sonnet-5" {
		t.Fatalf("SubagentModel = %q, want the append-time guess preserved", s.blocks[0].meta.SubagentModel)
	}
}

func TestSubagentCostFromResult_ParsesRealFormat(t *testing.T) {
	cost, ok := subagentCostFromResult(subagentResultWithCost)
	if !ok || cost != 0.0071 {
		t.Fatalf("cost=%v ok=%v, want 0.0071/true", cost, ok)
	}
}

// TestSubagentCostFromResult_AbsentWhenNothingRecorded covers a run cancelled
// before its first billed call: subagent.go's Execute never appends "Cost:"
// in that case, and this must report ok=false, not a fabricated $0.0000.
func TestSubagentCostFromResult_AbsentWhenNothingRecorded(t *testing.T) {
	const content = "partial output\n\n---\nSubagent finished. 1 tool calls, 1 turns."
	if _, ok := subagentCostFromResult(content); ok {
		t.Fatal("want ok=false when the result text has no Cost: line")
	}
}

func TestSubagentCostFromResult_EmptyContent(t *testing.T) {
	if _, ok := subagentCostFromResult(""); ok {
		t.Fatal("want ok=false for empty content")
	}
}

// TestCompleteSubagentCard_ShowsCostOnce covers the direct (non-batched)
// completion path: agent_io.go's handleEvent passes ev.ToolResultContent
// straight through.
func TestCompleteSubagentCard_ShowsCostOnce(t *testing.T) {
	s := newScrollback(200)
	s.appendSubagentCard(1, "call-1", "explore", "Explore the flow", "glm-5.2:cloud")

	if !s.completeSubagentCard("call-1", subagentResultWithCost, "", 1000) {
		t.Fatal("completeSubagentCard did not match call-1")
	}
	var card *Block
	for i := range s.blocks {
		if s.blocks[i].kind == blockSubagent {
			card = &s.blocks[i]
		}
	}
	if card == nil {
		t.Fatal("no subagent card found")
	}
	if !card.meta.SubagentCostKnown || card.meta.SubagentCost != 0.0071 {
		t.Fatalf("meta = %+v, want SubagentCostKnown with 0.0071", card.meta)
	}
	out := layoutSubagentCard(card, 80)
	if len(out) == 0 || !strings.Contains(out[0].Text, "$0.0071") {
		t.Fatalf("card does not show cost: %+v", out)
	}
}

// TestCompleteSubagentCard_BatchedSubagentShowsCost is the batch-tool case:
// agent.CompleteBatchedSubagent (see tools.BatchTool.subagentDoneFn) reaches
// the exact same completeSubagentCard call with the nested call's own result
// content — this pins that a batched subagent's cost renders identically to
// a direct one, keyed by its synthetic BatchCallID.
func TestCompleteSubagentCard_BatchedSubagentShowsCost(t *testing.T) {
	s := newScrollback(200)
	s.appendSubagentCard(1, "call-batch.0", "explore", "Explore checkout flow", "glm-5.2:cloud")
	s.appendSubagentCard(2, "call-batch.1", "explore", "Explore payment flow", "glm-5.2:cloud")

	const result1 = "checkout done.\n\n---\nSubagent finished. 2 tool calls, 2 turns. Cost: $0.0040."
	if !s.completeSubagentCard("call-batch.1", result1, "", 500) {
		t.Fatal("completeSubagentCard did not match call-batch.1")
	}

	for i := range s.blocks {
		b := &s.blocks[i]
		if b.kind != blockSubagent {
			continue
		}
		switch b.meta.ProviderCallID {
		case "call-batch.1":
			if !b.meta.SubagentCostKnown || b.meta.SubagentCost != 0.0040 {
				t.Errorf("call-batch.1 meta = %+v, want cost 0.0040", b.meta)
			}
		case "call-batch.0":
			if b.meta.SubagentCostKnown {
				t.Errorf("call-batch.0 must still be unknown (not yet completed), got %+v", b.meta)
			}
		}
	}
}

// TestSubagentCard_RunningWidgetNeverShowsCost: cost is only known once the
// child is done — a still-running widget must never show one even if a stale
// SubagentCost lingered in meta from some earlier bug.
func TestSubagentCard_RunningWidgetNeverShowsCost(t *testing.T) {
	s := newScrollback(200)
	s.appendSubagentCard(1, "call-1", "explore", "Explore the flow", "glm-5.2:cloud")
	var card *Block
	for i := range s.blocks {
		if s.blocks[i].kind == blockSubagent {
			card = &s.blocks[i]
		}
	}
	card.meta.SubagentCost, card.meta.SubagentCostKnown = 1.23, true // simulate stale state
	out := layoutSubagentCard(card, 80)
	if len(out) == 0 || strings.Contains(out[0].Text, "$1.2300") {
		t.Fatalf("running card must not show cost: %+v", out)
	}
}
