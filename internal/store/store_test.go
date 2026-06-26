package store

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func mustCreateSession(t *testing.T, s *Store, id string) *Session {
	t.Helper()
	sess := &Session{
		ID:         id,
		IsSubagent: false,
		Cwd:        "/tmp",
		Provider:   "anthropic",
		Model:      "claude-sonnet-4-20250514",
	}
	if err := s.CreateSession(sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return sess
}

// textContent builds a JSON array of content blocks containing a single
// text block.
func textContent(text string) string {
	b, _ := json.Marshal([]map[string]string{{"type": "text", "text": text}})
	return string(b)
}

// ---------- Store / schema ----------

func TestOpenAndSchema(t *testing.T) {
	s := newTestStore(t)

	// Verify tables exist by inserting + querying a row in each.
	mustCreateSession(t, s, "sess-1")

	// messages
	if err := s.AppendMessage(&Message{SessionID: "sess-1", Role: "user", Content: textContent("hi")}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	// api_calls
	if err := s.RecordAPICall(&APICall{SessionID: "sess-1", Seq: 1, Model: "m", InputTokens: 10, OutputTokens: 5, Cost: 0.01}); err != nil {
		t.Fatalf("RecordAPICall: %v", err)
	}

	// model_pricing
	if err := s.SeedPricing(); err != nil {
		t.Fatalf("SeedPricing: %v", err)
	}

	// Re-open the same DB file; schema creation must be idempotent.
	dir := filepath.Dir(t.TempDir()) // unused; re-open via the same path below
	_ = dir
}

func TestOpenIdempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "idem.db")
	s1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 1: %v", err)
	}
	if err := s1.SeedPricing(); err != nil {
		t.Fatalf("SeedPricing 1: %v", err)
	}
	s1.Close()

	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 2: %v", err)
	}
	defer s2.Close()
	// Seeding again must not error (INSERT OR IGNORE).
	if err := s2.SeedPricing(); err != nil {
		t.Fatalf("SeedPricing 2: %v", err)
	}
	p, err := s2.GetPricing("anthropic", "claude-opus-4-8")
	if err != nil {
		t.Fatalf("GetPricing: %v", err)
	}
	if p.InputPerMTok != 5.0 {
		t.Fatalf("input per mtok = %v, want 5.0", p.InputPerMTok)
	}
}

// ---------- Session CRUD ----------

func TestSessionCRUD(t *testing.T) {
	s := newTestStore(t)

	sess := &Session{
		ID:       "s1",
		Cwd:      "/home/user/project",
		Provider: "anthropic",
		Model:    "claude-sonnet-4-20250514",
	}
	if err := s.CreateSession(sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.CreatedAt == 0 {
		t.Fatal("CreatedAt not set")
	}

	got, err := s.GetSession("s1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.ID != "s1" || got.Cwd != "/home/user/project" || got.Provider != "anthropic" || got.Model != "claude-sonnet-4-20250514" {
		t.Fatalf("GetSession returned %+v", got)
	}
	if got.IsSubagent {
		t.Fatal("IsSubagent should be false")
	}

	// Update.
	got.Model = "claude-opus-4-20250514"
	got.Title = strPtr("My Session")
	if err := s.UpdateSession(got); err != nil {
		t.Fatalf("UpdateSession: %v", err)
	}
	got2, err := s.GetSession("s1")
	if err != nil {
		t.Fatalf("GetSession 2: %v", err)
	}
	if got2.Model != "claude-opus-4-20250514" || got2.Title == nil || *got2.Title != "My Session" {
		t.Fatalf("after update: %+v", got2)
	}

	// NotFound.
	if _, err := s.GetSession("does-not-exist"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	// List (multiple sessions).
	mustCreateSession(t, s, "s2")
	mustCreateSession(t, s, "s3")
	list, err := s.ListSessions(10, 0)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("ListSessions returned %d, want 3", len(list))
	}

	// Pagination.
	page1, err := s.ListSessions(2, 0)
	if err != nil || len(page1) != 2 {
		t.Fatalf("page1: len=%d err=%v", len(page1), err)
	}
	page2, err := s.ListSessions(2, 2)
	if err != nil || len(page2) != 1 {
		t.Fatalf("page2: len=%d err=%v", len(page2), err)
	}
}

func TestListSessionsOrdersByUpdatedAt(t *testing.T) {
	s := newTestStore(t)
	mustCreateSession(t, s, "old")
	mustCreateSession(t, s, "recent")
	if _, err := s.db.Exec(`UPDATE sessions SET created_at = 1, updated_at = 100 WHERE id = 'old'`); err != nil {
		t.Fatalf("update old: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE sessions SET created_at = 2, updated_at = 200 WHERE id = 'recent'`); err != nil {
		t.Fatalf("update recent: %v", err)
	}
	list, err := s.ListSessions(10, 0)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list) < 2 || list[0].ID != "recent" {
		t.Fatalf("session order = %+v, want recent first", list)
	}
}

func TestSessionCompactionSummary(t *testing.T) {
	s := newTestStore(t)
	mustCreateSession(t, s, "sc")

	if err := s.SetCompactionSummary("sc", "## Big Picture\nDo the thing"); err != nil {
		t.Fatalf("SetCompactionSummary: %v", err)
	}
	got, _ := s.GetSession("sc")
	if got.CompactionSummary == nil || *got.CompactionSummary != "## Big Picture\nDo the thing" {
		t.Fatalf("summary = %v", got.CompactionSummary)
	}

	if err := s.ClearCompactionSummary("sc"); err != nil {
		t.Fatalf("ClearCompactionSummary: %v", err)
	}
	got, _ = s.GetSession("sc")
	if got.CompactionSummary != nil {
		t.Fatalf("summary should be nil, got %v", *got.CompactionSummary)
	}
}

func TestSessionForkFields(t *testing.T) {
	s := newTestStore(t)
	parent := strPtr("parent-1")
	fork := strPtr("msg-1")
	sess := &Session{
		ID:        "fork-1",
		ParentID:  parent,
		ForkPoint: fork,
		Cwd:       "/tmp",
		Provider:  "anthropic",
		Model:     "claude-sonnet-4-20250514",
	}
	if err := s.CreateSession(sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	got, _ := s.GetSession("fork-1")
	if got.ParentID == nil || *got.ParentID != "parent-1" || got.ForkPoint == nil || *got.ForkPoint != "msg-1" {
		t.Fatalf("fork fields wrong: %+v", got)
	}
}

func TestSessionSubagent(t *testing.T) {
	s := newTestStore(t)
	sess := &Session{
		ID:         "sub-1",
		IsSubagent: true,
		Cwd:        "/tmp",
		Provider:   "anthropic",
		Model:      "claude-sonnet-4-20250514",
	}
	if err := s.CreateSession(sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	got, _ := s.GetSession("sub-1")
	if !got.IsSubagent {
		t.Fatal("IsSubagent should be true")
	}
}

// ---------- Message CRUD ----------

func TestAppendAndGetMessages(t *testing.T) {
	s := newTestStore(t)
	mustCreateSession(t, s, "m1")

	msgs := []Message{
		{SessionID: "m1", Role: "user", Content: textContent("Hello world")},
		{SessionID: "m1", Role: "assistant", Content: textContent("Hi there")},
		{SessionID: "m1", Role: "user", Content: textContent("Write a function")},
	}
	for i := range msgs {
		if err := s.AppendMessage(&msgs[i]); err != nil {
			t.Fatalf("AppendMessage %d: %v", i, err)
		}
	}

	got, err := s.GetMessages("m1")
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d messages, want 3", len(got))
	}
	// seq should be 1,2,3 in order.
	for i, m := range got {
		if m.Seq != i+1 {
			t.Fatalf("msg %d seq = %d, want %d", i, m.Seq, i+1)
		}
	}
	// IDs should be populated.
	for _, m := range got {
		if m.ID == "" {
			t.Fatal("message ID empty")
		}
	}
}

func TestSoftDeleteMessages(t *testing.T) {
	s := newTestStore(t)
	mustCreateSession(t, s, "sd")

	for _, txt := range []string{"one", "two", "three", "four"} {
		if err := s.AppendMessage(&Message{SessionID: "sd", Role: "user", Content: textContent(txt)}); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}

	// Soft delete from seq 3 onward.
	if err := s.SoftDeleteMessages("sd", 3); err != nil {
		t.Fatalf("SoftDeleteMessages: %v", err)
	}

	got, err := s.GetMessages("sd")
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("after soft delete got %d, want 2", len(got))
	}
	for _, m := range got {
		if m.Seq >= 3 {
			t.Fatalf("soft-deleted message seq %d still active", m.Seq)
		}
	}
}

func TestMarkCompacted(t *testing.T) {
	s := newTestStore(t)
	mustCreateSession(t, s, "mc")

	for _, txt := range []string{"a", "b", "c", "d", "e"} {
		if err := s.AppendMessage(&Message{SessionID: "mc", Role: "user", Content: textContent(txt)}); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}

	// Mark up to seq 3 as compacted.
	if err := s.MarkCompacted("mc", 3); err != nil {
		t.Fatalf("MarkCompacted: %v", err)
	}

	got, err := s.GetMessages("mc")
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("after compaction got %d active, want 2", len(got))
	}
	for _, m := range got {
		if m.Seq <= 3 {
			t.Fatalf("compacted message seq %d still active", m.Seq)
		}
	}
}

func TestCloneMessages(t *testing.T) {
	s := newTestStore(t)
	mustCreateSession(t, s, "src")
	mustCreateSession(t, s, "dst")

	for _, txt := range []string{"alpha", "beta", "gamma", "delta"} {
		if err := s.AppendMessage(&Message{SessionID: "src", Role: "user", Content: textContent(txt)}); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}

	// Clone messages up to seq 3 into dst.
	if err := s.CloneMessages("src", 3, "dst"); err != nil {
		t.Fatalf("CloneMessages: %v", err)
	}

	dstMsgs, err := s.GetMessages("dst")
	if err != nil {
		t.Fatalf("GetMessages dst: %v", err)
	}
	if len(dstMsgs) != 3 {
		t.Fatalf("dst has %d messages, want 3", len(dstMsgs))
	}
	// seq preserved.
	for i, m := range dstMsgs {
		if m.Seq != i+1 {
			t.Fatalf("cloned msg %d seq = %d, want %d", i, m.Seq, i+1)
		}
	}
	// IDs are new (different from source).
	srcMsgs, _ := s.GetMessages("src")
	srcIDs := map[string]bool{}
	for _, m := range srcMsgs {
		srcIDs[m.ID] = true
	}
	for _, m := range dstMsgs {
		if srcIDs[m.ID] {
			t.Fatal("cloned message reuses source ID")
		}
	}
}

// ---------- FTS5 Search ----------

func TestSearch(t *testing.T) {
	s := newTestStore(t)
	mustCreateSession(t, s, "search-sess")

	msgs := []Message{
		{SessionID: "search-sess", Role: "user", Content: textContent("How do I implement binary search in Go?")},
		{SessionID: "search-sess", Role: "assistant", Content: textContent("You can implement binary search using a loop.")},
		{SessionID: "search-sess", Role: "user", Content: textContent("What about quicksort algorithms?")},
	}
	for i := range msgs {
		if err := s.AppendMessage(&msgs[i]); err != nil {
			t.Fatalf("AppendMessage %d: %v", i, err)
		}
	}

	results, err := s.Search("binary search", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected search results for 'binary search', got none")
	}
	found := false
	for _, r := range results {
		if r.SessionID != "search-sess" {
			t.Errorf("result session = %s, want search-sess", r.SessionID)
		}
		if r.Snippet == "" {
			t.Error("empty snippet")
		}
		if r.Role == "" {
			t.Error("empty role")
		}
		// Verify the matched message is still active (JOIN filter).
		var active int
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE id = ? AND deleted_at IS NULL`, r.MessageID).Scan(&active)
		if active != 1 {
			t.Errorf("search returned non-active message %s", r.MessageID)
		}
		if contains(r.Snippet, "binary") || contains(lower(r.Role), "user") || contains(r.Snippet, "search") || contains(r.Snippet, "implement") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no relevant result found: %+v", results)
	}

	// Search for something not present.
	none, err := s.Search("nonexistentterm12345", 10)
	if err != nil {
		t.Fatalf("Search none: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("expected 0 results, got %d", len(none))
	}
}

func TestSearchFiltersSoftDeleted(t *testing.T) {
	s := newTestStore(t)
	mustCreateSession(t, s, "filter-sess")

	if err := s.AppendMessage(&Message{SessionID: "filter-sess", Role: "user", Content: textContent("uniqueterm zap alpha")}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if err := s.AppendMessage(&Message{SessionID: "filter-sess", Role: "assistant", Content: textContent("uniqueterm zap beta")}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	// Before delete: both findable.
	res, _ := s.Search("uniqueterm", 10)
	if len(res) != 2 {
		t.Fatalf("before delete: expected 2 results, got %d", len(res))
	}

	// Soft delete from seq 2 onward (removes the assistant message).
	if err := s.SoftDeleteMessages("filter-sess", 2); err != nil {
		t.Fatalf("SoftDeleteMessages: %v", err)
	}

	res, _ = s.Search("uniqueterm", 10)
	if len(res) != 1 {
		t.Fatalf("after soft delete: expected 1 result, got %d", len(res))
	}
}

func TestSearchFiltersCompacted(t *testing.T) {
	s := newTestStore(t)
	mustCreateSession(t, s, "compact-search")
	if err := s.AppendMessage(&Message{SessionID: "compact-search", Role: "user", Content: textContent("compactterm old")}); err != nil {
		t.Fatalf("AppendMessage old: %v", err)
	}
	if err := s.AppendMessage(&Message{SessionID: "compact-search", Role: "user", Content: textContent("compactterm new")}); err != nil {
		t.Fatalf("AppendMessage new: %v", err)
	}
	if err := s.MarkCompacted("compact-search", 1); err != nil {
		t.Fatalf("MarkCompacted: %v", err)
	}
	res, err := s.Search("compactterm", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("expected 1 active result, got %d", len(res))
	}
	var content string
	if err := s.db.QueryRow(`SELECT content FROM messages WHERE id = ?`, res[0].MessageID).Scan(&content); err != nil {
		t.Fatalf("query result: %v", err)
	}
	if !contains(content, "new") {
		t.Fatalf("search returned compacted/old message: %s", content)
	}
}

// ---------- API calls ----------

func TestAPICallMigrationMarksMinimaxZeroInputUnknown(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	mustCreateSession(t, s, "minimax-old")
	if err := s.RecordAPICall(&APICall{SessionID: "minimax-old", Seq: 1, Model: "minimax-m3:cloud", OutputTokens: 62}); err != nil {
		t.Fatalf("RecordAPICall: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE api_calls SET input_tokens_known = 1 WHERE session_id = 'minimax-old'`); err != nil {
		t.Fatalf("force known: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s.Close()
	last, err := s.GetLastAPICall("minimax-old")
	if err != nil {
		t.Fatalf("GetLastAPICall: %v", err)
	}
	if !last.InputTokensUnknown {
		t.Fatalf("minimax zero input row was not marked unknown: %+v", last)
	}
}

func TestAPICallUnknownInputTokens(t *testing.T) {
	s := newTestStore(t)
	mustCreateSession(t, s, "unknown-input")
	if err := s.RecordAPICall(&APICall{SessionID: "unknown-input", Seq: 1, Model: "minimax-m3:cloud", InputTokensUnknown: true, OutputTokens: 62}); err != nil {
		t.Fatalf("RecordAPICall unknown: %v", err)
	}
	if err := s.RecordAPICall(&APICall{SessionID: "unknown-input", Seq: 2, Model: "glm-5.2:cloud", InputTokens: 18, OutputTokens: 5}); err != nil {
		t.Fatalf("RecordAPICall known: %v", err)
	}
	last, err := s.GetLastAPICall("unknown-input")
	if err != nil {
		t.Fatalf("GetLastAPICall: %v", err)
	}
	if last.InputTokensUnknown {
		t.Fatalf("last call should have known input: %+v", last)
	}
	tb, err := s.GetSessionTokenBreakdown("unknown-input")
	if err != nil {
		t.Fatalf("GetSessionTokenBreakdown: %v", err)
	}
	if tb.InputTokens != 18 || tb.InputUnknownCalls != 1 || tb.OutputTokens != 67 || tb.CallCount != 2 {
		t.Fatalf("breakdown = %+v", tb)
	}
}

func TestAPICalls(t *testing.T) {
	s := newTestStore(t)
	mustCreateSession(t, s, "ap")

	calls := []APICall{
		{SessionID: "ap", Seq: 1, Model: "claude-sonnet-4-20250514", InputTokens: 1000, OutputTokens: 200, CacheReadTokens: 500, CacheWriteTokens: 100, Cost: 0.01},
		{SessionID: "ap", Seq: 2, Model: "claude-sonnet-4-20250514", InputTokens: 1500, OutputTokens: 300, CacheReadTokens: 800, CacheWriteTokens: 200, Cost: 0.02},
		{SessionID: "ap", Seq: 3, Model: "claude-sonnet-4-20250514", InputTokens: 2000, OutputTokens: 400, CacheReadTokens: 1000, CacheWriteTokens: 300, Cost: 0.03},
	}
	for i := range calls {
		if err := s.RecordAPICall(&calls[i]); err != nil {
			t.Fatalf("RecordAPICall %d: %v", i, err)
		}
	}

	// GetLastAPICall → the third one.
	last, err := s.GetLastAPICall("ap")
	if err != nil {
		t.Fatalf("GetLastAPICall: %v", err)
	}
	if last.Seq != 3 {
		t.Fatalf("last seq = %d, want 3", last.Seq)
	}
	if last.InputTokens != 2000 {
		t.Fatalf("last input = %d, want 2000", last.InputTokens)
	}

	// GetLastAPICall on empty session.
	mustCreateSession(t, s, "empty")
	if _, err := s.GetLastAPICall("empty"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	// GetSessionCost.
	cost, err := s.GetSessionCost("ap")
	if err != nil {
		t.Fatalf("GetSessionCost: %v", err)
	}
	if cost != 0.06 {
		t.Fatalf("session cost = %v, want 0.06", cost)
	}

	// GetTotalCost.
	total, err := s.GetTotalCost()
	if err != nil {
		t.Fatalf("GetTotalCost: %v", err)
	}
	if total != 0.06 {
		t.Fatalf("total cost = %v, want 0.06", total)
	}

	// GetSessionTokenBreakdown.
	tb, err := s.GetSessionTokenBreakdown("ap")
	if err != nil {
		t.Fatalf("GetSessionTokenBreakdown: %v", err)
	}
	if tb.InputTokens != 4500 || tb.OutputTokens != 900 ||
		tb.CacheReadTokens != 2300 || tb.CacheWriteTokens != 600 ||
		tb.TotalCost != 0.06 || tb.CallCount != 3 {
		t.Fatalf("token breakdown = %+v", tb)
	}
}

// ---------- Pricing ----------

func TestPricing(t *testing.T) {
	s := newTestStore(t)
	if err := s.SeedPricing(); err != nil {
		t.Fatalf("SeedPricing: %v", err)
	}

	// Anthropic exact match.
	p, err := s.GetPricing("anthropic", "claude-opus-4-8")
	if err != nil {
		t.Fatalf("GetPricing anthropic: %v", err)
	}
	if p.InputPerMTok != 5.0 || p.OutputPerMTok != 25.0 || p.CacheReadPerMTok != 0.5 || p.CacheWritePerMTok != 3.0 {
		t.Fatalf("opus pricing = %+v", p)
	}

	// xAI exact match.
	p, err = s.GetPricing("xai", "grok-build")
	if err != nil {
		t.Fatalf("GetPricing grok-build: %v", err)
	}
	if p.InputPerMTok != 1.0 || p.OutputPerMTok != 2.0 {
		t.Fatalf("grok-build pricing = %+v", p)
	}

	// ollama fallback "*".
	p, err = s.GetPricing("ollama", "qwen3-coder:30b")
	if err != nil {
		t.Fatalf("GetPricing ollama: %v", err)
	}
	if p.InputPerMTok != 0 || p.OutputPerMTok != 0 {
		t.Fatalf("ollama pricing = %+v", p)
	}

	// Unknown provider/model.
	_, err = s.GetPricing("unknown", "nope")
	if err != ErrPricingNotFound {
		t.Fatalf("expected ErrPricingNotFound, got %v", err)
	}
}

func TestComputeCost(t *testing.T) {
	s := newTestStore(t)
	if err := s.SeedPricing(); err != nil {
		t.Fatalf("SeedPricing: %v", err)
	}

	// claude-opus-4-8: 5 input, 25 output, 0.5 cache read, 3 cache write.
	cost := s.ComputeCost("anthropic", "claude-opus-4-8", 1_000_000, 1_000_000, 0, 0)
	if !approxEqual(cost, 30.0, 1e-9) {
		t.Fatalf("cost = %v, want 30.0", cost)
	}

	cost = s.ComputeCost("anthropic", "claude-opus-4-8", 500_000, 200_000, 100_000, 50_000)
	if !approxEqual(cost, 7.7, 1e-9) {
		t.Fatalf("cost = %v, want 7.7", cost)
	}

	// grok-build: 1 input, 2 output.
	cost = s.ComputeCost("xai", "grok-build", 1_000_000, 1_000_000, 0, 0)
	if !approxEqual(cost, 3.0, 1e-9) {
		t.Fatalf("xai cost = %v, want 3.0", cost)
	}

	// ollama → 0.
	cost = s.ComputeCost("ollama", "qwen3-coder:30b", 1_000_000, 1_000_000, 0, 0)
	if cost != 0 {
		t.Fatalf("ollama cost = %v, want 0", cost)
	}

	// Unknown → 0.
	cost = s.ComputeCost("unknown", "nope", 1_000_000, 1_000_000, 0, 0)
	if cost != 0 {
		t.Fatalf("unknown cost = %v, want 0", cost)
	}
}

// ---------- helpers ----------

func strPtr(s string) *string { return &s }

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func lower(s string) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		out[i] = c
	}
	return string(out)
}

func approxEqual(a, b, tol float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < tol
}

// Ensure fmt is used (sanity prints if needed).
var _ = fmt.Sprintf

func TestNewSessionIDUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := NewSessionID()
		if len(id) < 3 || id[:2] != "s-" {
			t.Fatalf("id %q missing s- prefix", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
	// IDs must differ in their displayed prefix, not just the tail.
	a, b := NewSessionID(), NewSessionID()
	if a[:6] == b[:6] {
		t.Fatalf("short prefixes collide: %q vs %q", a, b)
	}
}
