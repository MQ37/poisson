package tui

import (
	"testing"

	"github.com/mq37/poisson/internal/agent"
)

// TestApplyInferenceSpeedTagsRoundBlocks verifies a round's thinking,
// assistant-text, and tool-call blocks all get tagged with the round's
// tok/s once applyInferenceSpeed fires (see agent.OutputInferenceSpeed),
// and that a later round's blocks aren't retroactively tagged by an earlier
// round's figure — the pending set must actually reset between rounds.
func TestApplyInferenceSpeedTagsRoundBlocks(t *testing.T) {
	s := newScrollback(1024)

	// Round 1: thinking + assistant text + one tool call.
	s.appendBlock(blockThinking, "reasoning...")
	s.finalizeThinking()
	s.appendBlock(blockAssistant, "here is my answer")
	s.appendToolCall(1, "call_1", "bash", []byte(`{"command":"echo hi"}`))

	if len(s.pendingSpeedBlocks) != 3 {
		t.Fatalf("pendingSpeedBlocks = %d, want 3 (thinking, assistant, tool call)", len(s.pendingSpeedBlocks))
	}
	s.applyInferenceSpeed(42.5)

	if len(s.blocks) != 3 {
		t.Fatalf("blocks = %d, want 3", len(s.blocks))
	}
	for i, b := range s.blocks {
		if b.meta.TokensPerSec != 42.5 {
			t.Errorf("block %d (%v) TokensPerSec = %v, want 42.5", i, b.kind, b.meta.TokensPerSec)
		}
	}
	if s.pendingSpeedBlocks != nil {
		t.Fatal("pendingSpeedBlocks should be cleared after applyInferenceSpeed")
	}

	// Round 2: a fresh assistant block. Must not be retroactively tagged by
	// round 1's already-applied figure, and applying round 2's own figure
	// must not disturb round 1's blocks.
	s.appendBlock(blockAssistant, "a second, separate reply")
	if len(s.pendingSpeedBlocks) != 1 {
		t.Fatalf("pendingSpeedBlocks = %d, want 1 (only round 2's new block)", len(s.pendingSpeedBlocks))
	}
	if got := s.blocks[3].meta.TokensPerSec; got != 0 {
		t.Fatalf("round 2 block tagged before its own applyInferenceSpeed call: %v", got)
	}
	s.applyInferenceSpeed(10)
	if s.blocks[3].meta.TokensPerSec != 10 {
		t.Fatalf("round 2 block TokensPerSec = %v, want 10", s.blocks[3].meta.TokensPerSec)
	}
	for i := 0; i < 3; i++ {
		if s.blocks[i].meta.TokensPerSec != 42.5 {
			t.Errorf("round 1 block %d disturbed by round 2's applyInferenceSpeed: %v", i, s.blocks[i].meta.TokensPerSec)
		}
	}
}

// TestPendingSpeedBlocksClearOnTurnEndEvenWithoutAReading reproduces a real
// bug: a turn can end WITHOUT ever reaching either sendInferenceSpeedEvent
// call site in runTurn — e.g. cancellation mid-stream
// (persistPartialTurnOnCancel) or a non-retryable/exhausted-retry mid-stream
// error both return after content already streamed (and its blocks already
// markRoundBlock'd) — yet agent.OutputDone is sent on every one of runTurn's
// exit paths with no exception. Relying only on sendInferenceSpeedEvent to
// clear scrollback's pendingSpeedBlocks left these aborted rounds' blocks
// stuck forever, so the NEXT round's real reading wrongly retagged them too.
// The fix clears pendingSpeedBlocks unconditionally on OutputDone (see
// markAfterEvent), independent of whether a speed reading ever arrived.
func TestPendingSpeedBlocksClearOnTurnEndEvenWithoutAReading(t *testing.T) {
	e := newTUIIntegEnv(t, nil)

	// Round 1: streams partial content, then aborts (mid-stream error, same
	// shape as runTurn's non-retryable EventError exit path) without ever
	// sending OutputInferenceSpeed.
	e.feedEvent(agent.OutputEvent{Type: agent.OutputText, Text: "partial answer"})
	e.feedEvent(agent.OutputEvent{Type: agent.OutputError, Text: "boom"})
	e.feedEvent(agent.OutputEvent{Type: agent.OutputDone})

	if pending := len(e.tui.scroll.pendingSpeedBlocks); pending != 0 {
		t.Fatalf("pendingSpeedBlocks after aborted round = %d, want 0 (OutputDone must always clear it)", pending)
	}
	abortedIdx := e.firstBlockOfKind(blockAssistant)
	if abortedIdx < 0 {
		t.Fatal("expected the aborted round's partial assistant block to exist")
	}

	// Round 2: a fresh round with a real reading — must tag only its OWN
	// block, not round 1's abandoned one.
	e.feedEvent(agent.OutputEvent{Type: agent.OutputText, Text: "second answer"})
	e.feedEvent(agent.OutputEvent{Type: agent.OutputInferenceSpeed, OutputTokens: 20, TokensPerSec: 99})
	e.feedEvent(agent.OutputEvent{Type: agent.OutputDone})

	if got := e.tui.scroll.blocks[abortedIdx].meta.TokensPerSec; got != 0 {
		t.Fatalf("round 1's aborted block was wrongly retagged by round 2: TokensPerSec = %v, want 0", got)
	}
}

// TestApplyInferenceSpeedNoopWhenNothingPending covers the plain no-tool-call
// path (a round-boundary call with nothing accumulated, or a stray/duplicate
// event) — must not panic or touch any block.
func TestApplyInferenceSpeedNoopWhenNothingPending(t *testing.T) {
	s := newScrollback(1024)
	s.appendBlock(blockUser, "hi")
	s.applyInferenceSpeed(99) // nothing pending: blockUser was never marked
	if s.blocks[0].meta.TokensPerSec != 0 {
		t.Fatalf("unrelated block tagged: %v", s.blocks[0].meta.TokensPerSec)
	}
}

func TestFormatThinkingCollapsedWithSpeed(t *testing.T) {
	withSpeed := formatThinkingCollapsed(120, 2300, 55)
	if !containsPlain(withSpeed, "55 tok/s") {
		t.Errorf("expected tok/s in %q", withSpeed)
	}
	withoutSpeed := formatThinkingCollapsed(120, 2300, 0)
	if containsPlain(withoutSpeed, "tok/s") {
		t.Errorf("expected no tok/s in %q", withoutSpeed)
	}
}

func TestToolCardSpeedSuffix(t *testing.T) {
	b := &Block{meta: BlockMeta{TokensPerSec: 123.4}}
	if got := toolCardSpeedSuffix(b); got != " · 123 tok/s" {
		t.Errorf("suffix = %q, want %q", got, " · 123 tok/s")
	}
	zero := &Block{meta: BlockMeta{TokensPerSec: 0}}
	if got := toolCardSpeedSuffix(zero); got != "" {
		t.Errorf("suffix for zero speed = %q, want empty", got)
	}
}

// TestBlockAssistantRendersTokensPerSec verifies the final answer text block
// grows a trailing "tok/s" line only once its round's speed is known —
// resumed-session / still-streaming blocks (TokensPerSec == 0) must render
// exactly as before this feature, with no stray suffix line.
func TestBlockAssistantRendersTokensPerSec(t *testing.T) {
	plain := Block{id: 1, kind: blockAssistant, raw: "hello"}
	plainRows := plain.layoutPlain(40)
	for _, r := range plainRows {
		if containsPlain(r.Text, "tok/s") {
			t.Errorf("unexpected tok/s in plain block row: %q", r.Text)
		}
	}

	withSpeed := Block{id: 2, kind: blockAssistant, raw: "hello", meta: BlockMeta{TokensPerSec: 87}}
	rows := withSpeed.layoutPlain(40)
	if len(rows) <= len(plainRows) {
		t.Fatalf("expected an extra row for the tok/s suffix, got %d rows (plain had %d)", len(rows), len(plainRows))
	}
	last := rows[len(rows)-1].Text
	if !containsPlain(last, "87 tok/s") {
		t.Errorf("last row = %q, want it to mention 87 tok/s", last)
	}
}
