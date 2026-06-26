package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"poisson/internal/provider"
	"poisson/internal/store"
)

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

// compact performs mid-turn compaction of the conversation.
func (a *Agent) compact() error {
	// Send "compacting" status event.
	a.sendEvent(OutputEvent{Type: OutputCompacting, Text: "compacting context..."})

	// 1. Collect active messages.
	msgs, err := a.store.GetMessages(a.sessionID)
	if err != nil {
		return fmt.Errorf("get messages: %w", err)
	}
	if len(msgs) == 0 {
		return nil
	}

	// 2. Determine how many to summarize (overflow handling).
	window := a.ContextWindow()
	threshold50 := window / 2
	estimatedTokens := 0
	for _, m := range msgs {
		estimatedTokens += a.EstimateTokens(m.Content)
	}

	summarizeCount := len(msgs)
	if estimatedTokens > threshold50 {
		// Summarize only the oldest half.
		summarizeCount = len(msgs) / 2
		if summarizeCount < 1 {
			summarizeCount = 1
		}
	}

	toSummarize := msgs[:summarizeCount]

	// 3. Build summarization request.
	summarizationMsgs := make([]provider.Message, 0, len(toSummarize)+1)
	for _, m := range toSummarize {
		pm, err := messageToProvider(m)
		if err != nil {
			return fmt.Errorf("convert message %s: %w", m.ID, err)
		}
		summarizationMsgs = append(summarizationMsgs, pm)
	}

	// Prepend a user message instructing summarization.
	summarizationMsgs = append([]provider.Message{{
		Role: "user",
		Content: []provider.ContentBlock{{
			Type: "text",
			Text: "Summarize the following conversation for context handoff. Produce ONLY the structured summary.",
		}},
	}}, summarizationMsgs...)

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
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	ch, err := a.provider.Stream(ctx, req)
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

	// 5. Mark old messages as compacted.
	upToSeq := toSummarize[len(toSummarize)-1].Seq
	if err := a.store.MarkCompacted(a.sessionID, upToSeq); err != nil {
		return fmt.Errorf("mark compacted: %w", err)
	}

	// 6. Store summary on session.
	if err := a.store.SetCompactionSummary(a.sessionID, summaryText); err != nil {
		return fmt.Errorf("set compaction summary: %w", err)
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
		if err := a.store.RecordAPICall(call); err != nil {
			return fmt.Errorf("record compaction api call: %w", err)
		}
	}

	// 8. Record compaction row.
	if err := a.store.RecordCompaction(&store.Compaction{
		SessionID:    a.sessionID,
		MessageID:    nil, // nullable after undo
		Summary:      summaryText,
		TokensBefore: estimatedTokens,
		CreatedAt:    time.Now().Unix(),
	}); err != nil {
		return fmt.Errorf("record compaction: %w", err)
	}

	// 9. Clear pending results.
	a.pendingResults = nil

	return nil
}

// shouldCompact checks if compaction should trigger.
func (a *Agent) shouldCompact() bool {
	return a.ShouldCompact()
}
