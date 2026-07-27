package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/config"
	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/store"
)

// padSummary pads body with filler text so it clears minSummaryChars — tests
// that don't assert on the summary's exact content use a short literal for
// readability; this keeps that literal from tripping compact()'s degenerate-
// summary floor.
func padSummary(body string) string {
	for len(body) < minSummaryChars {
		body += " filler"
	}
	return body
}

// seedMessages appends messages with the given roles to a session.
func seedMessages(t *testing.T, s *store.Store, sid string, roles []string) {
	t.Helper()
	now := time.Now().Unix()
	for i, role := range roles {
		content, _ := json.Marshal([]map[string]string{{"type": "text", "text": role + " msg"}})
		if err := s.AppendMessage(&store.Message{
			SessionID: sid, Role: role, Content: string(content), CreatedAt: now + int64(i),
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// TestCompactRetriesMidStreamOverload verifies compaction gets the same
// mid-stream resilience a turn has: a retryable provider error arriving
// after HTTP 200 (which provider.DoWithRetry structurally cannot see) is
// retried instead of failing the whole compaction, which would otherwise
// leave the conversation over budget with nothing done about it.
func TestCompactRetriesMidStreamOverload(t *testing.T) {
	shrinkRetryBackoffs(t)
	s := newTestStore(t)
	sid := newTestSession(t, s, "test-model")
	seedMessages(t, s, sid, []string{"user", "assistant", "user", "assistant"})

	fp := newFakeProvider()
	fp.SetResponses([][]provider.StreamEvent{
		{{Type: provider.EventError, Error: errOverloaded, Retryable: true}},
		provider.FakeTextResponse(padSummary("## Big Picture\nAll compacted"), nil),
	})
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, make(chan OutputEvent, 8), func(context.Context, string, string, string) (bool, string) { return true, "" })

	if err := a.Compact(); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if fp.CallCount() != 2 {
		t.Errorf("provider calls = %d, want 2 (one overload + one retry)", fp.CallCount())
	}
	msgs, err := s.GetMessages(sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Fatalf("active messages = %d, want 0 (whole conversation compacted despite the retry)", len(msgs))
	}
}

// TestCompactRetriesEmptyResponse verifies an empty summarization reply (a
// transient provider glitch) is retried rather than immediately failing
// compaction with "compaction produced empty summary".
func TestCompactRetriesEmptyResponse(t *testing.T) {
	shrinkRetryBackoffs(t)
	s := newTestStore(t)
	sid := newTestSession(t, s, "test-model")
	seedMessages(t, s, sid, []string{"user", "assistant", "user", "assistant"})

	fp := newFakeProvider()
	fp.SetResponses([][]provider.StreamEvent{
		{{Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 5, OutputTokens: 0}}},
		provider.FakeTextResponse(padSummary("## Big Picture\nAll compacted"), nil),
	})
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, make(chan OutputEvent, 8), func(context.Context, string, string, string) (bool, string) { return true, "" })

	if err := a.Compact(); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if fp.CallCount() != 2 {
		t.Errorf("provider calls = %d, want 2 (one empty response + one retry)", fp.CallCount())
	}
}

// TestCompactRecordsUsagePerRetriedAttempt verifies a retried compaction
// attempt still gets its tokens recorded — not just the final successful
// one — since a retried attempt spent real tokens against the provider too.
func TestCompactRecordsUsagePerRetriedAttempt(t *testing.T) {
	shrinkRetryBackoffs(t)
	s := newTestStore(t)
	sid := newTestSession(t, s, "test-model")
	seedMessages(t, s, sid, []string{"user", "assistant", "user", "assistant"})

	fp := newFakeProvider()
	fp.SetResponses([][]provider.StreamEvent{
		{
			// Usage arrives (e.g. a final accounting event) before the
			// stream ultimately errors out — the failed attempt still cost
			// real tokens and must still be recorded.
			{Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 100, OutputTokens: 1}},
			{Type: provider.EventError, Error: errOverloaded, Retryable: true},
		},
		provider.FakeTextResponse(padSummary("## Big Picture\nAll compacted"), nil),
	})
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, make(chan OutputEvent, 8), func(context.Context, string, string, string) (bool, string) { return true, "" })

	if err := a.Compact(); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	// No main turns ran in this session, so every recorded api_calls row is
	// this compaction — one per provider attempt.
	tb, err := s.GetSessionTokenBreakdown(sid)
	if err != nil {
		t.Fatal(err)
	}
	if tb.CallCount != 2 {
		t.Errorf("recorded api_calls = %d, want 2 (failed attempt + successful retry)", tb.CallCount)
	}
	if tb.InputTokens != 110 { // 100 from the failed attempt + 10 from the (default-usage) retry
		t.Errorf("recorded input tokens = %d, want 110 (both attempts counted)", tb.InputTokens)
	}
}

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

// TestCompactManualKeepsUserFirst guards against a leading assistant/tool
// message in the kept tail after a budget-limited manual /compact. The summary
// is a separate system block, so Messages must begin with a user turn or the
// provider (Anthropic) rejects the next request.
func TestCompactionRuntimeParsesCrossProviderTarget(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := newTestStore(t)
	sid := newTestSession(t, s, "test-model")
	cfg := newTestConfig()
	cfg.Compaction.Model = "ollama/summarizer"
	a := NewAgent(s, provider.NewFakeProvider("fake", nil), nil, cfg, sid, nil, nil)

	p, model, err := a.compactionRuntime()
	if err != nil {
		t.Fatal(err)
	}
	if p.ID() != "ollama" || model != "summarizer" {
		t.Fatalf("runtime = %s/%s, want ollama/summarizer", p.ID(), model)
	}
}

func TestCompactionAPICallStoresTargetProvider(t *testing.T) {
	s := newTestStore(t)
	sid := newTestSession(t, s, "test-model")
	cfg := newTestConfig()
	cfg.Pricing = map[string]map[string]config.Pricing{
		"ollama": {"summarizer": {InputPerMTok: 1, OutputPerMTok: 2}},
	}
	a := NewAgent(s, provider.NewFakeProvider("fake", nil), nil, cfg, sid, nil, nil)

	if err := a.recordCompactionAPICall("ollama", "summarizer", &provider.Usage{InputTokens: 10, OutputTokens: 5}); err != nil {
		t.Fatal(err)
	}
	var providerID, model string
	if err := s.DB().QueryRow(`SELECT provider, model FROM api_calls WHERE session_id = ?`, sid).Scan(&providerID, &model); err != nil {
		t.Fatal(err)
	}
	if providerID != "ollama" || model != "summarizer" {
		t.Fatalf("stored = %s/%s, want ollama/summarizer", providerID, model)
	}
}

func TestCompactManualKeepsUserFirst(t *testing.T) {
	s := newTestStore(t)
	sid := newTestSession(t, s, "test-model")
	big := strings.Repeat("x ", 2400) // ~960 tokens/msg so 6 exceed the 0.65*8192 budget but 3 don't
	roles := []string{"user", "assistant", "user", "assistant", "user", "assistant"}
	now := time.Now().Unix()
	for i, role := range roles {
		content, _ := json.Marshal([]map[string]string{{"type": "text", "text": role + " " + big}})
		if err := s.AppendMessage(&store.Message{
			SessionID: sid, Role: role, Content: string(content), CreatedAt: now + int64(i),
		}); err != nil {
			t.Fatal(err)
		}
	}
	fp := newFakeProvider()
	fp.SetResponses([][]provider.StreamEvent{provider.FakeTextResponse(padSummary("summary"), nil)})
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, make(chan OutputEvent, 8), func(context.Context, string, string, string) (bool, string) { return true, "" })
	if err := a.Compact(); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	msgs, err := s.GetMessages(sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) > 0 && msgs[0].Role != "user" {
		t.Fatalf("kept tail starts with %q, want user (valid request start); kept %d of %d", msgs[0].Role, len(msgs), len(roles))
	}
}

// TestChooseSummarizeCountBudgetsAgainstCompactionModelWindow guards the
// context-window budgeting fix: chooseSummarizeCount must size its budget
// against the model that actually RECEIVES the summarization request
// (config.Compaction.Model's model, here "small-model" — same "fake"
// provider as the main model, so compactionRuntime resolves it without
// needing a second real provider), not a.ContextWindow() (the main
// conversation model, "big-model", whose enormous window would make every
// message fit and never split). Regressing to the old behavior would leave
// nothing active after compaction; the fix must still leave some tail active.
func TestChooseSummarizeCountBudgetsAgainstCompactionModelWindow(t *testing.T) {
	s := newTestStore(t)
	sid := newTestSession(t, s, "big-model")
	cfg := newTestConfig()
	cfg.Compaction.Model = "fake/small-model"

	// Same sizing as TestCompactManualKeepsUserFirst: ~1200 tokens/message, so
	// 6 exceed a 0.65*8192 budget but fewer don't — proven to force a real
	// split against an 8192-token window.
	big := strings.Repeat("x ", 2400)
	roles := []string{"user", "assistant", "user", "assistant", "user", "assistant"}
	now := time.Now().Unix()
	for i, role := range roles {
		content, _ := json.Marshal([]map[string]string{{"type": "text", "text": role + " " + big}})
		if err := s.AppendMessage(&store.Message{
			SessionID: sid, Role: role, Content: string(content), CreatedAt: now + int64(i),
		}); err != nil {
			t.Fatal(err)
		}
	}

	fp := provider.NewFakeProvider("fake", []provider.Model{
		{ID: "big-model", ContextWindow: 10_000_000},
		{ID: "small-model", ContextWindow: 8192},
	})
	fp.SetResponses([][]provider.StreamEvent{provider.FakeTextResponse(padSummary("## Big Picture\nsplit"), nil)})
	a := NewAgent(s, fp, newTestRegistry("."), cfg, sid, make(chan OutputEvent, 8), func(context.Context, string, string, string) (bool, string) { return true, "" })

	if err := a.Compact(); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	reqs := fp.Requests()
	if len(reqs) != 1 || reqs[0].Model != "small-model" {
		t.Fatalf("compaction request model = %+v, want a single request against small-model", reqs)
	}
	msgs, err := s.GetMessages(sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) == 0 {
		t.Fatal("all 6 messages were summarized in one pass — budget used big-model's huge window " +
			"instead of the compaction model's (small-model, 8192 tokens); expected a split leaving some active")
	}
}

// TestCompactionRequestAlwaysEndsWithUserMessage reproduces a real 400 from
// Anthropic's newer models: "This model does not support assistant message
// prefill. The conversation must end with a user message." toSummarize (the
// messages being fed to the summarizer, as opposed to the kept tail) ends
// wherever the boundary search happens to land — usually an assistant or tool
// message, since the search only guarantees the KEPT tail starts with "user",
// not that the summarized transcript ENDS with one. The summarization request
// is a separate provider.Request built from scratch, so it must independently
// satisfy the same "ends with user" rule any Anthropic request does.
func TestCompactionRequestAlwaysEndsWithUserMessage(t *testing.T) {
	s := newTestStore(t)
	sid := newTestSession(t, s, "test-model")
	// assistant, tool, assistant: the boundary search stops at the next "user"
	// (there isn't one), so summarizeCount == len(msgs) and toSummarize's last
	// message is the trailing assistant reply — exactly what triggers the 400.
	seedMessages(t, s, sid, []string{"user", "assistant", "tool", "assistant"})

	fp := newFakeProvider()
	fp.SetResponses([][]provider.StreamEvent{
		provider.FakeTextResponse(padSummary("## Big Picture\nsummary"), nil),
	})
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, make(chan OutputEvent, 8), func(context.Context, string, string, string) (bool, string) { return true, "" })

	if err := a.Compact(); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	req := fp.LastRequest()
	if req == nil || len(req.Messages) == 0 {
		t.Fatal("expected a captured compaction request")
	}
	if last := req.Messages[len(req.Messages)-1]; last.Role != "user" {
		t.Fatalf("compaction request's last message role = %q, want %q (Anthropic rejects a request ending in assistant role as unsupported message prefill)", last.Role, "user")
	}
}

func TestCompactSummarizesAllMessages(t *testing.T) {
	s := newTestStore(t)
	sid := newTestSession(t, s, "test-model")
	seedMessages(t, s, sid, []string{"user", "assistant", "user", "assistant"})

	fp := newFakeProvider()
	fp.SetResponses([][]provider.StreamEvent{
		provider.FakeTextResponse(padSummary("## Big Picture\nAll compacted"), nil),
	})
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, make(chan OutputEvent, 8), func(context.Context, string, string, string) (bool, string) { return true, "" })

	if err := a.Compact(); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	msgs, err := s.GetMessages(sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Fatalf("active messages = %d, want 0 (whole conversation compacted)", len(msgs))
	}
}

// TestCompactRecordsSummaryTokensNotZero reproduces a reported UX bug: after
// a manual /compact that summarizes the whole conversation (nothing left in
// the active set), the notice showed "191,321 -> 0 tokens" — a nonsensical
// after-count, since the compaction summary itself (what's ACTUALLY carried
// forward into every future request, per agent.go's system-block injection)
// costs real tokens. The old code summed only the active messages' tokens,
// ignoring the summary entirely whenever it emptied the active set to zero.
func TestCompactRecordsSummaryTokensNotZero(t *testing.T) {
	s := newTestStore(t)
	sid := newTestSession(t, s, "test-model")
	seedMessages(t, s, sid, []string{"user", "assistant", "user", "assistant"})

	summary := "## Big Picture\n" + strings.Repeat("detail ", 200) // long enough for a non-trivial token estimate
	fp := newFakeProvider()
	fp.SetResponses([][]provider.StreamEvent{provider.FakeTextResponse(summary, nil)})
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, make(chan OutputEvent, 8), func(context.Context, string, string, string) (bool, string) { return true, "" })

	if err := a.Compact(); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	msgs, err := s.GetMessages(sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Fatalf("active messages = %d, want 0 (precondition: whole conversation compacted)", len(msgs))
	}

	want := a.EstimateTokens(summary)
	comp, err := s.GetLastCompaction(sid)
	if err != nil || comp == nil {
		t.Fatalf("GetLastCompaction: %v, %+v", err, comp)
	}
	if comp.TokensAfter != want {
		t.Fatalf("TokensAfter = %d, want %d (the summary's own token cost, not 0)", comp.TokensAfter, want)
	}
}

// TestSecondCompactionIncorporatesPreviousSummary covers a normal, common
// occurrence in any long session — a SECOND /compact after the first already
// left a compaction summary — which no existing test ever exercised: every
// other compaction test seeds a session with no prior summary, so the
// "incorporate the previous summary, don't blindly preserve everything"
// instruction-building branch in compact() was completely untested. It's
// exactly the class of gap that produced two real bugs earlier this session
// (the request-shape and the token-count bugs), just in a different branch.
func TestSecondCompactionIncorporatesPreviousSummary(t *testing.T) {
	s := newTestStore(t)
	sid := newTestSession(t, s, "test-model")
	seedMessages(t, s, sid, []string{"user", "assistant"})

	firstSummary := padSummary("## Big Picture\nfirst summary content")
	secondSummary := padSummary("## Big Picture\nsecond summary content")
	fp := newFakeProvider()
	fp.SetResponses([][]provider.StreamEvent{
		provider.FakeTextResponse(firstSummary, nil),
		provider.FakeTextResponse(secondSummary, nil),
	})
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, make(chan OutputEvent, 8), func(context.Context, string, string, string) (bool, string) { return true, "" })

	if err := a.Compact(); err != nil {
		t.Fatalf("first Compact: %v", err)
	}
	sess, err := s.GetSession(sid)
	if err != nil || sess == nil || sess.CompactionSummary == nil || *sess.CompactionSummary != firstSummary {
		t.Fatalf("after first compact, session summary = %+v, want %q", sess, firstSummary)
	}

	// Simulate continued conversation after the first compaction.
	seedMessages(t, s, sid, []string{"user", "assistant"})

	if err := a.Compact(); err != nil {
		t.Fatalf("second Compact: %v", err)
	}

	reqs := fp.Requests()
	if len(reqs) != 2 {
		t.Fatalf("Stream calls = %d, want 2", len(reqs))
	}
	found := false
	for _, m := range reqs[1].Messages {
		for _, c := range m.Content {
			if strings.Contains(c.Text, firstSummary) {
				found = true
			}
		}
	}
	if !found {
		t.Error("second compaction's request never included the previous summary in its instruction")
	}

	// The summary must be REPLACED, not accumulated/duplicated across compactions.
	sess2, err := s.GetSession(sid)
	if err != nil || sess2 == nil || sess2.CompactionSummary == nil {
		t.Fatalf("GetSession after second compact: %v, %+v", err, sess2)
	}
	if strings.Contains(*sess2.CompactionSummary, "first summary content") {
		t.Errorf("second compaction's summary should replace the first, not contain it: %q", *sess2.CompactionSummary)
	}
	if !strings.Contains(*sess2.CompactionSummary, "second summary content") {
		t.Errorf("summary after second compact = %q, want the new summary", *sess2.CompactionSummary)
	}

	// The second compaction's "before" figure must include the FIRST summary's
	// tokens, not just the newly-seeded messages' — the fix for the "0 tokens"
	// bug touched this same estimatedTokens computation.
	comp, err := s.GetLastCompaction(sid)
	if err != nil || comp == nil {
		t.Fatalf("GetLastCompaction: %v, %+v", err, comp)
	}
	if comp.TokensBefore < a.EstimateTokens(firstSummary) {
		t.Errorf("second compaction's TokensBefore = %d, should be at least the first summary's own token cost (%d)", comp.TokensBefore, a.EstimateTokens(firstSummary))
	}
}

// TestCompactAutoKeepsActiveTail verifies auto-compaction never empties the
// active set: it summarizes everything before the current turn and keeps that
// turn (starting with a user message) active, so the run loop's next request is
// valid and non-empty.
func TestCompactAutoKeepsActiveTail(t *testing.T) {
	s := newTestStore(t)
	sid := newTestSession(t, s, "test-model")
	seedMessages(t, s, sid, []string{"user", "assistant", "user", "assistant", "tool"})

	fp := newFakeProvider()
	fp.SetResponses([][]provider.StreamEvent{
		provider.FakeTextResponse(padSummary("## Big Picture\nolder turns"), nil),
	})
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, make(chan OutputEvent, 8), func(context.Context, string, string, string) (bool, string) { return true, "" })

	if err := a.compact(context.Background(), false, true); err != nil {
		t.Fatalf("compact: %v", err)
	}
	msgs, err := s.GetMessages(sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 {
		t.Fatalf("active messages = %d, want 3 (current turn kept)", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Fatalf("first active message role = %q, want user (valid request start)", msgs[0].Role)
	}
}

// TestCompactAutoNothingWhenSingleTurn verifies auto-compaction bails without
// touching messages when only the current turn exists (nothing older to
// summarize), rather than emptying the active set.
// TestCompactAutoAppendsContinuationOnSingleTurn covers the normal subagent
// trajectory: one task, then an extended tool-calling run with no further
// "user" message to split at. Auto-compaction must still be able to trigger
// (summarize everything) instead of silently doing nothing forever — it
// appends a synthetic user turn so the next request still opens with a user
// message.
func TestCompactAutoAppendsContinuationOnSingleTurn(t *testing.T) {
	s := newTestStore(t)
	sid := newTestSession(t, s, "test-model")
	seedMessages(t, s, sid, []string{"user", "assistant", "tool"})

	fp := newFakeProvider()
	fp.SetResponses([][]provider.StreamEvent{provider.FakeTextResponse(padSummary("## Big Picture\nDid stuff."), nil)})
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, make(chan OutputEvent, 8), func(context.Context, string, string, string) (bool, string) { return true, "" })

	if err := a.compact(context.Background(), false, true); err != nil {
		t.Fatalf("compact = %v, want success", err)
	}
	sess, err := s.GetSession(sid)
	if err != nil || sess == nil || sess.CompactionSummary == nil || *sess.CompactionSummary == "" {
		t.Fatal("expected a compaction summary to be stored")
	}
	msgs, err := s.GetMessages(sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Role != "user" {
		t.Fatalf("active messages = %+v, want exactly one synthetic user continuation", msgs)
	}
}
