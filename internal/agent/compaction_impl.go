package agent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"poisson/internal/provider"
	"poisson/internal/store"
)

// ErrNothingToCompact is returned when /compact has no messages to summarize.
var ErrNothingToCompact = errors.New("nothing to compact")

// compactionSystemPrompt is the handoff summarization prompt from SPEC §13.3.
const compactionSystemPrompt = `You are a context summarization assistant. Your task is to read a conversation between a user and an AI assistant, then produce a structured summary following the exact format specified.

Do NOT continue the conversation. Do NOT respond to any questions in the conversation. ONLY output the structured summary.

Produce a summary with these sections:

## Big Picture
What is the overall goal of this conversation? What is the user trying to accomplish?

## Key Decisions
Important decisions made, approaches chosen, and why.

## Current State
What has been done so far. What files were created, modified, or examined. What commands were run and their outcomes.

## User Instructions
Any specific instructions, constraints, or preferences the user has stated. Preserve these verbatim if possible.

## Pending Tasks
What remains to be done. If the conversation was interrupted mid-task, describe exactly where things left off so the next agent can continue seamlessly.

## Important Details
Any small but critical details: file paths, error messages, environment quirks, version numbers, or anything that would be hard to rediscover.`

// Compact triggers manual compaction (/compact). Does not emit UI events; the TUI
// owns scrollback refresh for manual compaction.
func (a *Agent) Compact() error {
	return a.compact(context.Background(), false, false)
}

// compact performs compaction of the conversation. When notifyUI is true it
// emits compacting/compacted events for the TUI run loop. When keepActiveTail
// is true (auto-compaction during a turn, where the loop re-streams straight
// after) it always leaves the current turn active so the next request stays
// valid and non-empty.
func (a *Agent) compact(ctx context.Context, notifyUI, keepActiveTail bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if notifyUI {
		a.sendEvent(OutputEvent{Type: OutputCompacting, Text: "compacting context..."})
	}

	// 1. Collect active messages.
	msgs, err := a.store.GetMessages(a.sessionID)
	if err != nil {
		return fmt.Errorf("get messages: %w", err)
	}
	if len(msgs) == 0 {
		return ErrNothingToCompact
	}

	// 2. Choose how many leading messages to summarize.
	estimatedTokens := 0
	for _, m := range msgs {
		estimatedTokens += a.EstimateTokens(m.Content)
	}

	summarizeCount, err := a.chooseSummarizeCount(msgs, keepActiveTail)
	if err != nil {
		return err
	}

	toSummarize := msgs[:summarizeCount]

	// 3. Build summarization request.
	summarizationMsgs := make([]provider.Message, 0, len(toSummarize)+2)
	instruction := "Summarize the following conversation for context handoff. Produce ONLY the structured summary."
	// If there is a previous summary, include it as context but instruct the
	// model to incorporate relevant details and omit stale ones — NOT to
	// blindly preserve everything (that caused unbounded summary growth across
	// repeated compactions). The model summarizes fresh, naturally compressing.
	if sess, err := a.store.GetSession(a.sessionID); err == nil && sess != nil &&
		sess.CompactionSummary != nil && strings.TrimSpace(*sess.CompactionSummary) != "" {
		instruction += "\n\nA previous context summary is included below. Incorporate relevant details and omit stale or superseded ones — do not blindly preserve everything.\n\n[Previous summary]\n" + *sess.CompactionSummary
	}
	summarizationMsgs = append(summarizationMsgs, provider.Message{
		Role: "user",
		Content: []provider.ContentBlock{{
			Type: "text",
			Text: instruction,
		}},
	})
	for _, m := range toSummarize {
		pm, err := messageToProvider(m)
		if err != nil {
			return fmt.Errorf("convert message %s: %w", m.ID, err)
		}
		// Never feed image bytes to the summarizer (huge, and it can't use them);
		// replace image blocks with a text placeholder.
		for i := range pm.Content {
			if pm.Content[i].Type == "image" {
				pm.Content[i] = provider.ContentBlock{Type: "text", Text: "[image]"}
			}
		}
		summarizationMsgs = append(summarizationMsgs, pm)
	}

	compactionModel := a.currentModel()
	if a.config != nil && a.config.Compaction.Model != "" {
		compactionModel = a.config.Compaction.Model
	}

	req := &provider.Request{
		Model:    compactionModel,
		System:   []provider.SystemBlock{{Text: compactionSystemPrompt}},
		Messages: summarizationMsgs,
	}

	// 4. Stream the summary.
	streamCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	ch, err := a.provider.Stream(streamCtx, req)
	if err != nil {
		return fmt.Errorf("compaction stream: %w", err)
	}

	var summary strings.Builder
	var usage *provider.Usage
	for ev := range ch {
		switch ev.Type {
		case provider.EventTextDelta:
			summary.WriteString(ev.Text)
		case provider.EventDone:
			usage = ev.Usage
		case provider.EventError:
			return fmt.Errorf("compaction error: %w", ev.Error)
		}
	}

	summaryText := strings.TrimSpace(summary.String())
	if summaryText == "" {
		return fmt.Errorf("compaction produced empty summary")
	}

	// 5–6. Atomically store summary and mark messages compacted.
	upToSeq := toSummarize[len(toSummarize)-1].Seq
	if err := a.store.ApplyCompaction(a.sessionID, upToSeq, summaryText); err != nil {
		return fmt.Errorf("apply compaction: %w", err)
	}

	// keepActiveTail normally leaves messages after the last user turn active.
	// When summarizeCount consumed everything (no later user message existed to
	// split at), the active set is now empty and the next request would open
	// with no messages at all. Append a synthetic user turn so it stays valid
	// — the same pattern appendContinueMessage uses for max-tokens continuation.
	if keepActiveTail && summarizeCount == len(msgs) {
		if err := a.appendContinueMessage(); err != nil {
			return fmt.Errorf("append post-compaction continuation: %w", err)
		}
	}

	// 7. Record api_call for summarization (exact tokens + cost).
	if usage != nil {
		_ = a.recordCompactionAPICall(compactionModel, usage)
	}

	// 8. Record compaction row (audit).
	remainingTokens := 0
	if remain, err := a.store.GetMessages(a.sessionID); err == nil {
		for _, m := range remain {
			remainingTokens += a.EstimateTokens(m.Content)
		}
	}
	compCost := 0.0
	if usage != nil {
		compCost = a.computeCost(a.provider.ID(), compactionModel,
			usage.InputTokens, usage.OutputTokens, usage.CacheReadTokens, usage.CacheWriteTokens)
	}
	if err := a.store.RecordCompaction(&store.Compaction{
		ID:           store.NewSessionID(),
		SessionID:    a.sessionID,
		Summary:      summaryText,
		TokensBefore: estimatedTokens,
		TokensAfter:  remainingTokens,
		Cost:         compCost,
		CreatedAt:    time.Now().Unix(),
	}); err != nil {
		log.Printf("warning: record compaction: %v", err)
	}

	// 9. Clear the compaction backoff. Reset the context-file tracker: the
	// injected AGENTS.md now live in summarized-away messages, so they must be
	// re-injected the next time those directories are touched.
	a.compactBackoffUntil = time.Time{}
	a.resetContextTracker()
	a.UpdateStatus()

	if notifyUI {
		a.sendEvent(OutputEvent{
			Type:                   OutputCompacted,
			CompactionTokensBefore: estimatedTokens,
			CompactionTokensAfter:  remainingTokens,
		})
	}

	return nil
}

// shouldCompact checks if compaction should trigger.
func (a *Agent) shouldCompact() bool {
	return a.ShouldCompact()
}

// chooseSummarizeCount decides how many leading messages to summarize. With
// keepActiveTail set (auto-compaction mid-turn) it summarizes everything before
// the current turn and keeps that turn active: a fresh request's first message
// must be a user turn and the in-flight tool results must stay visible. Without
// it (manual /compact) it summarizes as much as fits the summarization budget,
// which may empty the active set — the next user message then starts fresh.
func (a *Agent) chooseSummarizeCount(msgs []store.Message, keepActiveTail bool) (int, error) {
	if keepActiveTail {
		count := lastUserIndex(msgs)
		if count <= 0 {
			// No completed prior turn to split at: the whole history is one
			// continuous tool-calling run since the single task/prompt that
			// started it (the normal subagent trajectory — one task, many tool
			// rounds, no further "user" messages in between). Summarize
			// everything; compact() appends a synthetic user turn afterward so
			// the next request still opens with a user message.
			return len(msgs), nil
		}
		return count, nil
	}

	count := adjustCompactionCount(msgs, len(msgs))
	budget := int(float64(a.ContextWindow()) * 0.65)
	var summary string
	if sess, err := a.store.GetSession(a.sessionID); err == nil && sess != nil &&
		sess.CompactionSummary != nil {
		summary = *sess.CompactionSummary
	}
	for count > 1 {
		est := a.EstimateTokens(summary)
		for _, m := range msgs[:count] {
			est += a.EstimateTokens(m.Content)
		}
		if est <= budget {
			break
		}
		count = adjustCompactionCount(msgs, count/2)
	}
	// The kept tail (msgs[count:]) becomes the request's Messages array — the
	// summary is a separate system block — so it must begin with a user turn.
	// Anthropic (and others) reject a leading assistant/tool message. Advance
	// the boundary to the next user message (or the end, leaving an empty tail).
	for count < len(msgs) && msgs[count].Role != "user" {
		count++
	}
	if count <= 0 {
		return 0, ErrNothingToCompact
	}
	return count, nil
}

// lastUserIndex returns the index of the most recent user message, or -1 if
// there is none.
func lastUserIndex(msgs []store.Message) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return i
		}
	}
	return -1
}

// adjustCompactionCount ensures we don't split assistant/tool_use from tool results.
func adjustCompactionCount(msgs []store.Message, count int) int {
	if count <= 0 {
		return 0
	}
	if count > len(msgs) {
		count = len(msgs)
	}
	for count < len(msgs) && msgs[count].Role == "tool" {
		count++
	}
	return count
}
