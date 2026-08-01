package agent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/store"
)

// ErrNothingToCompact is returned when /compact has no messages to summarize.
var ErrNothingToCompact = errors.New("nothing to compact")

// minSummaryChars is the floor a cleaned summary must clear to be accepted.
// Below this it's treated the same as an empty response — refused rather
// than committed as the sole surviving memory of the summarized messages.
const minSummaryChars = 200

// compactionSystemPrompt is the handoff summarization prompt (originally
// SPEC §13.3, since rewritten for detail — a short, generic summary that
// merely "covers the topics" loses far more than it should: the next agent
// gets ONLY this text, never the original messages, so anything vague or
// omitted here is permanently gone, not just compressed).
const compactionSystemPrompt = `You are a context-handoff summarizer. A fresh AI agent with NO memory of this conversation will receive ONLY the summary you produce — never the original messages — and must resume the work seamlessly from it alone.

Do NOT continue the conversation. Do NOT respond to any questions in the conversation. Do NOT ask the user anything. ONLY output the structured summary below.

Be thorough, not brief. Match the summary's length and density to the conversation's actual substance — a long, detailed session deserves a long, detailed summary; do not compress for brevity's own sake. When unsure whether a detail matters, include it: the next agent can skip something extra, but can never recover something missing.

Preserve concrete specifics verbatim wherever they appear — never flattened into vague generalities:
- Exact file paths touched (created/edited/read/deleted), each one, not "several files were updated".
- Exact shell commands run and their outcome — exit status, key output lines, error text — not "ran the tests" but e.g. "ran go test ./..., all green" or "ran npm run build, failed with: <exact error>".
- Exact function/variable/config names, error messages, stack traces, version numbers, URLs, IDs, ports, paths.
- The user's own instructions, corrections, and stated preferences, quoted verbatim wherever possible — not paraphrased into your own words.
- Bad: "Fixed a bug in the auth module." Good: "Fixed internal/auth/token.go:42 — expiry check used < instead of <=, so tokens were rejected exactly at their expiry instant."

Produce a summary with these sections:

## Big Picture
The overall goal of this conversation — what the user is ultimately trying to accomplish, and why (the underlying problem or motivation), not just the immediate task.

## Key Decisions
Every non-obvious decision made and the reasoning behind it: approaches chosen over alternatives, trade-offs accepted, things explicitly rejected and why. Include enough of the "why" that the next agent doesn't undo or re-litigate something already settled.

## Current State
Exactly what has been done, in enough detail to avoid redoing or re-discovering it: every file created/modified/examined (with paths), every command run (with its outcome), every external effect (commits, deployments, API calls, config/data changes). Chronological order if that helps clarity.

## User Instructions
Every specific instruction, constraint, or preference the user stated, quoted verbatim. Don't drop one for seeming minor — an explicit "don't do X" or "always do Y this way" must survive compaction exactly, word for word.

## Pending Tasks
What remains to be done. If the conversation was interrupted mid-task, describe exactly where things left off: the specific file/line/command in progress, the specific error being debugged, the specific next step already decided — enough that the next agent continues from that exact point without re-reading anything.

## Important Details
Anything else that would be hard or costly to rediscover: environment quirks, gotchas, version numbers, exact error message text, tricky edge cases already worked out, and things that were already tried and did NOT work (so they aren't retried).`

// Compact triggers manual compaction (/compact). Does not emit UI events; the TUI
// owns scrollback refresh for manual compaction.
func (a *Agent) Compact() error {
	return a.compact(context.Background(), false, false)
}

func (a *Agent) compactionRuntime() (provider.Provider, string, error) {
	target := ""
	if a.config != nil {
		target = strings.TrimSpace(a.config.Compaction.Model)
	}
	if target == "" {
		return a.provider, a.currentModel(), nil
	}

	providerID, model, qualified := strings.Cut(target, "/")
	if !qualified {
		return a.provider, target, nil
	}
	providerID, model = strings.TrimSpace(providerID), strings.TrimSpace(model)
	if providerID == "" || model == "" {
		return nil, "", fmt.Errorf("invalid compaction model %q; use provider/model", target)
	}
	if providerID == a.providerID() {
		return a.provider, model, nil
	}
	p := provider.NewProviderFromDisk(providerID, a.config)
	if p == nil {
		return nil, "", fmt.Errorf("unknown compaction provider %q", providerID)
	}
	return p, model, nil
}

// compact performs compaction of the conversation. When notifyUI is true it
// emits compacting/compacted events for the TUI run loop. When keepActiveTail
// is true (auto-compaction during a turn, where the loop re-streams straight
// after) it always leaves the current turn active so the next request stays
// valid and non-empty.
func (a *Agent) compact(ctx context.Context, notifyUI, keepActiveTail bool) error {
	// Serialize against any other compact() call on this Agent (manual
	// /compact vs. auto-compaction from the turn loop) — see compactMu's
	// doc on agent.go. Held for the whole function so a second caller
	// blocks until the first's summary is fully applied, then reads the
	// post-compaction state instead of racing against it.
	a.compactMu.Lock()
	defer a.compactMu.Unlock()

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

	// 2. Choose how many leading messages to summarize. estimatedTokens is the
	// "before" figure reported to the user, so it must include any pre-existing
	// compaction summary too (not just active messages) — that summary is real
	// context the model already carries on every request, per agent.go's
	// system-block injection.
	sessBefore, _ := a.store.GetSession(a.sessionID)
	estimatedTokens := a.summaryTokens(sessBefore)
	for _, m := range msgs {
		estimatedTokens += a.EstimateTokens(m.Content)
	}

	// Resolve which provider/model actually receives the summarization
	// request BEFORE budgeting the summarize count against it — the
	// compaction model (config.Compaction.Model) can have a smaller context
	// window than the conversation's main model, and budgeting against the
	// wrong (larger) window can hand the compaction call itself more
	// messages than it can fit.
	compactionProvider, compactionModel, err := a.compactionRuntime()
	if err != nil {
		return err
	}

	summarizeCount, err := a.chooseSummarizeCount(msgs, keepActiveTail, compactionProvider, compactionModel)
	if err != nil {
		return err
	}

	toSummarize := msgs[:summarizeCount]

	// 2b. The messages NOT being summarized (msgs[summarizeCount:]) stay
	// active and keep being sent in full on every future turn until some
	// later compaction eventually reaches them. Shrink any `read` among them
	// that a later call in that same surviving window has already made
	// stale — see compaction_prune.go for why doing it right here is free.
	if kept := msgs[summarizeCount:]; len(kept) > 0 {
		cwd := a.cwd()
		if sessBefore != nil && sessBefore.Cwd != "" {
			cwd = sessBefore.Cwd
		}
		a.pruneStaleToolResults(cwd, kept)
	}

	// 3. Build summarization request.
	summarizationMsgs := make([]provider.Message, 0, len(toSummarize)+2)
	instruction := "Summarize the following conversation for context handoff. Produce ONLY the structured summary."
	// If there is a previous summary, include it as context but instruct the
	// model to incorporate relevant details and omit stale ones — NOT to
	// blindly preserve everything (that caused unbounded summary growth across
	// repeated compactions). The model summarizes fresh, naturally compressing.
	if sessBefore != nil && sessBefore.CompactionSummary != nil && strings.TrimSpace(*sessBefore.CompactionSummary) != "" {
		instruction += "\n\nA previous context summary is included below. Incorporate relevant details and omit stale or superseded ones — do not blindly preserve everything.\n\n[Previous summary]\n" + *sessBefore.CompactionSummary
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
	// toSummarize's last message can be any role — the boundary search only
	// guarantees the KEPT tail starts with "user", not that the summarized
	// transcript ENDS with one. This is a separate request built from scratch,
	// so it must independently end with a user message or Anthropic's newer
	// models reject it: "This model does not support assistant message
	// prefill. The conversation must end with a user message."
	summarizationMsgs = append(summarizationMsgs, provider.Message{
		Role: "user",
		Content: []provider.ContentBlock{{
			Type: "text",
			Text: "Now produce the structured summary as instructed above. Do not continue the conversation.",
		}},
	})

	req := &provider.Request{
		Model:    compactionModel,
		System:   []provider.SystemBlock{{Text: compactionSystemPrompt}},
		Messages: summarizationMsgs,
	}

	// 4. Stream the summary. streamAndCollect gives this the same mid-stream
	// resilience a turn has — a retryable provider error or an empty
	// response arriving after HTTP 200 (which provider.DoWithRetry
	// structurally cannot see) is retried instead of losing the whole
	// compaction attempt outright. Transport failures and retryable statuses
	// are already retried inside Stream itself. usage is recorded per
	// attempt (a retried attempt still spent real tokens) and the last
	// attempt's usage is kept for the cost/audit fields below.
	streamCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	var usage *provider.Usage
	out, err := streamAndCollect(streamCtx, compactionProvider, req, func(u *provider.Usage) {
		usage = u
		if err := a.recordCompactionAPICall(compactionProvider.ID(), compactionModel, u); err != nil {
			log.Printf("warning: record compaction API call: %v", err)
		}
	})
	if err != nil {
		return fmt.Errorf("compaction stream: %w", err)
	}

	summaryText := strings.TrimSpace(out.Text)
	if summaryText == "" {
		return fmt.Errorf("compaction produced empty summary")
	}
	// A non-empty but suspiciously short summary is just as dangerous as an
	// empty one: it becomes the ONLY surviving memory of everything in
	// toSummarize, and a truncated/malformed response (stream cut off, model
	// error, provider hiccup) can pass the empty check while still losing
	// nearly everything. minSummaryChars is a coarse floor, not a quality
	// bar — legitimate summaries of even a small conversation clear it
	// easily given the prompt's mandatory section headers.
	if n := utf8.RuneCountInString(summaryText); n < minSummaryChars {
		return fmt.Errorf("compaction produced a suspiciously short summary (%d chars, want >= %d) — refusing to apply; likely a truncated or malformed model response", n, minSummaryChars)
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

	// 7. api_call already recorded per attempt inside streamAndCollect's
	// onUsage callback above (step 4) — a retried attempt still spent real
	// tokens, so recording only the final one would silently drop them.

	// 8. Record compaction row (audit). The "after" figure must include the
	// summary just applied, not just active messages — otherwise summarizing
	// the whole conversation (nothing left active) always reports a nonsensical
	// "0 tokens" even though the summary itself is what's actually carried
	// forward from here on.
	remainingTokens := a.estimateMessagesTokens()
	compCost := 0.0
	if usage != nil {
		compCost = a.computeCost(compactionProvider.ID(), compactionModel,
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
func (a *Agent) chooseSummarizeCount(msgs []store.Message, keepActiveTail bool, compactionProvider provider.Provider, compactionModel string) (int, error) {
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
	// Budget against the model that actually RECEIVES the summarization
	// request (compactionProvider/compactionModel), not a.ContextWindow()
	// (the main conversation model) — those differ whenever
	// config.Compaction.Model is set to a smaller-window model.
	budget := int(float64(a.contextWindowFor(compactionProvider, compactionModel)) * 0.65)
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

// adjustCompactionCount ensures we don't split assistant/tool_use from tool
// results, by walking count forward past any run of "tool"-role messages it
// lands inside. Guarantee chooseSummarizeCount's halving search depends on:
// if the input count already sits inside a tool run, the walk never returns
// something >= that run's start except when the run reaches len(msgs) with
// no boundary ahead — in that one case, retreat to the run's start instead of
// snapping forward to len(msgs). Without the retreat, a halving search that
// repeatedly lands inside a trailing tool run (common after a turn with
// several parallel tool calls) would walk forward to len(msgs) every time and
// never shrink, spinning forever.
func adjustCompactionCount(msgs []store.Message, count int) int {
	if count <= 0 {
		return 0
	}
	if count > len(msgs) {
		count = len(msgs)
	}
	start := count
	for count < len(msgs) && msgs[count].Role == "tool" {
		count++
	}
	if count == len(msgs) && count > start {
		count = start
		for count > 0 && msgs[count-1].Role == "tool" {
			count--
		}
	}
	return count
}
