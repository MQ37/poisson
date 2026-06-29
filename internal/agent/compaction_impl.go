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
	return a.compact(context.Background(), false)
}

// compact performs mid-turn compaction of the conversation. When notifyUI is
// true, emits compacting/compacted events for the TUI run loop.
func (a *Agent) compact(ctx context.Context, notifyUI bool) error {
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

	// 2. Summarize the entire active conversation.
	estimatedTokens := 0
	for _, m := range msgs {
		estimatedTokens += a.EstimateTokens(m.Content)
	}

	summarizeCount := adjustCompactionCount(msgs, len(msgs))
	if summarizeCount <= 0 {
		return ErrNothingToCompact
	}

	toSummarize := msgs[:summarizeCount]

	// 3. Build summarization request.
	summarizationMsgs := make([]provider.Message, 0, len(toSummarize)+2)
	instruction := "Summarize the following conversation for context handoff. Produce ONLY the structured summary."
	if sess, err := a.store.GetSession(a.sessionID); err == nil && sess != nil &&
		sess.CompactionSummary != nil && strings.TrimSpace(*sess.CompactionSummary) != "" {
		instruction = "You have a previous conversation summary. Merge it with the new messages below into ONE updated structured summary. Preserve all important details from the previous summary.\n\nPrevious summary:\n" +
			*sess.CompactionSummary + "\n\nNow merge with these additional messages:"
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

	// 7. Record api_call for summarization (exact tokens + cost).
	if usage != nil {
		providerID := a.provider.ID()
		cost := a.store.ComputeCost(providerID, compactionModel,
			usage.InputTokens, usage.OutputTokens, usage.CacheReadTokens, usage.CacheWriteTokens)
		call := &store.APICall{
			SessionID:          a.sessionID,
			Seq:                a.nextAPICallSeq(),
			Model:              compactionModel,
			InputTokens:        usage.InputTokens,
			InputTokensUnknown: usage.InputTokensUnknown,
			OutputTokens:       usage.OutputTokens,
			CacheReadTokens:    usage.CacheReadTokens,
			CacheWriteTokens:   usage.CacheWriteTokens,
			Cost:               cost,
			CreatedAt:          time.Now().Unix(),
		}
		_ = a.store.RecordAPICall(call)
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
		compCost = a.store.ComputeCost(a.provider.ID(), compactionModel,
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

	// 9. Clear pending results.
	a.pendingResults = nil

	if notifyUI {
		a.sendEvent(OutputEvent{Type: OutputCompacted})
	}

	return nil
}

// shouldCompact checks if compaction should trigger.
func (a *Agent) shouldCompact() bool {
	return a.ShouldCompact()
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
