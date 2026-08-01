package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/testutil"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := testutil.TempDir(t)
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

}

func TestListSessionsAllAndCounts(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < 60; i++ {
		mustCreateSession(t, s, "s"+strconv.Itoa(i))
	}
	if err := s.AppendMessage(&Message{SessionID: "s0", Role: "user", Content: textContent("a")}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage(&Message{SessionID: "s0", Role: "assistant", Content: textContent("b")}); err != nil {
		t.Fatal(err)
	}
	all, err := s.ListSessions(-1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 60 {
		t.Fatalf("ListSessions(-1) = %d, want 60 (all)", len(all))
	}
	def, _ := s.ListSessions(0, 0)
	if len(def) != 50 {
		t.Fatalf("ListSessions(0) = %d, want 50 (default)", len(def))
	}
	counts, err := s.MessageCountsBySession()
	if err != nil {
		t.Fatal(err)
	}
	if counts["s0"] != 2 {
		t.Fatalf("counts[s0] = %d, want 2", counts["s0"])
	}
}

func TestDeleteSession(t *testing.T) {
	s := newTestStore(t)
	mustCreateSession(t, s, "del-1")
	mustCreateSession(t, s, "keep-2")
	if err := s.AppendMessage(&Message{SessionID: "del-1", Role: "user", Content: textContent("zebrafish delete me")}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage(&Message{SessionID: "keep-2", Role: "user", Content: textContent("keep this one")}); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteSession("del-1"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := s.GetSession("del-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetSession(del-1) err = %v, want ErrNotFound", err)
	}
	if msgs, _ := s.GetMessages("del-1"); len(msgs) != 0 {
		t.Errorf("deleted session still has %d messages", len(msgs))
	}
	if res, _ := s.Search("zebrafish", 10); len(res) != 0 {
		t.Errorf("FTS still returns %d hits from the deleted session", len(res))
	}
	// The other session is untouched.
	if _, err := s.GetSession("keep-2"); err != nil {
		t.Errorf("keep-2 should survive delete: %v", err)
	}
	if msgs, _ := s.GetMessages("keep-2"); len(msgs) != 1 {
		t.Errorf("keep-2 should keep its 1 message, got %d", len(msgs))
	}
}

// TestDeleteSessionRemovesImageFiles is the regression guard for the fix
// that plugged the /tmp leak: an image content block's ImagePath used to
// survive forever on disk even after the session referencing it was
// deleted, since nothing ever unlinked it.
func TestDeleteSessionRemovesImageFiles(t *testing.T) {
	s := newTestStore(t)
	mustCreateSession(t, s, "del-img")

	dir := testutil.TempDir(t)
	imgPath := filepath.Join(dir, "pasted.png")
	if err := os.WriteFile(imgPath, []byte("fake png bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	content, _ := json.Marshal([]map[string]string{{"type": "image", "image_path": imgPath}})
	if err := s.AppendMessage(&Message{SessionID: "del-img", Role: "user", Content: string(content)}); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteSession("del-img"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := os.Stat(imgPath); !os.IsNotExist(err) {
		t.Errorf("image file should be removed after DeleteSession, stat err = %v", err)
	}
}

func TestAppendMessageRequiresSession(t *testing.T) {
	s := newTestStore(t)
	err := s.AppendMessage(&Message{SessionID: "missing-session", Role: "user", Content: textContent("hi")})
	if err == nil {
		t.Fatal("expected foreign key error when session does not exist")
	}
}

// TestAppendMessageConcurrentSameSessionAssignsDistinctSeq is the
// regression guard for the seq TOCTOU race: nextSeq's SELECT MAX(seq) used
// to run outside AppendMessage's transaction, so two concurrent callers on
// the same session could both read the same MAX before either INSERTed,
// assigning the same seq to two different messages and corrupting the
// ORDER BY seq ordering every reader (GetMessages, GetAllMessages, hydrate,
// /search) relies on. Moving the read inside the transaction (which the
// pool's single connection, SetMaxOpenConns(1), then holds exclusively
// until Commit/Rollback) closes it: every appended message must get a
// unique seq, with no gaps or duplicates, regardless of how many goroutines
// race to append.
func TestAppendMessageConcurrentSameSessionAssignsDistinctSeq(t *testing.T) {
	s := newTestStore(t)
	mustCreateSession(t, s, "concurrent-seq")

	const n = 50
	var wg sync.WaitGroup
	errs := make(chan error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			errs <- s.AppendMessage(&Message{SessionID: "concurrent-seq", Role: "user", Content: textContent(strconv.Itoa(i))})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}

	msgs, err := s.GetAllMessages("concurrent-seq")
	if err != nil {
		t.Fatalf("GetAllMessages: %v", err)
	}
	if len(msgs) != n {
		t.Fatalf("got %d messages, want %d", len(msgs), n)
	}
	seen := make(map[int]bool, n)
	for _, m := range msgs {
		if seen[m.Seq] {
			t.Fatalf("duplicate seq %d among appended messages — seq race not fixed", m.Seq)
		}
		seen[m.Seq] = true
	}
	for i := 1; i <= n; i++ {
		if !seen[i] {
			t.Errorf("missing seq %d — want a contiguous 1..%d range", i, n)
		}
	}
}

// regression guard for the api_calls.seq TOCTOU race — same bug class as
// AppendMessage's, fixed the same way: RecordAPICall now computes seq
// inside its own transaction (nextAPICallSeqTx) instead of the caller
// reading MAX(seq) outside any tx.
func TestRecordAPICallConcurrentSameSessionAssignsDistinctSeq(t *testing.T) {
	s := newTestStore(t)
	mustCreateSession(t, s, "concurrent-api-seq")

	const n = 50
	var wg sync.WaitGroup
	errs := make(chan error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			errs <- s.RecordAPICall(&APICall{SessionID: "concurrent-api-seq", Model: "m", InputTokens: i, OutputTokens: 1, Cost: 0.01})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("RecordAPICall: %v", err)
		}
	}

	rows, err := s.db.Query(`SELECT seq FROM api_calls WHERE session_id = ?`, "concurrent-api-seq")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	seen := make(map[int]bool, n)
	count := 0
	for rows.Next() {
		var seq int
		if err := rows.Scan(&seq); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if seen[seq] {
			t.Fatalf("duplicate seq %d among recorded api_calls — seq race not fixed", seq)
		}
		seen[seq] = true
		count++
	}
	if count != n {
		t.Fatalf("got %d api_calls rows, want %d", count, n)
	}
	for i := 1; i <= n; i++ {
		if !seen[i] {
			t.Errorf("missing seq %d — want a contiguous 1..%d range", i, n)
		}
	}
}

// TestSchemaEnforcesUniqueSeqDirectly verifies a fresh database (via the
// normal Open path) rejects a duplicate (session_id, seq) row outright —
// the unique index now lives directly in schemaSQL (this project has no
// released version with an on-disk schema old enough to need a migration
// for it; see the migrations var's doc comment), not behind a migration
// that only an upgrading, pre-existing database would ever run.
func TestSchemaEnforcesUniqueSeqDirectly(t *testing.T) {
	s := newTestStore(t)
	sess := mustCreateSession(t, s, "dup-seq")

	insert := func(seq int) error {
		_, err := s.db.Exec(
			`INSERT INTO api_calls (id, session_id, seq, model, input_tokens, output_tokens, cost, created_at)
			 VALUES (?,?,?,?,?,?,?,?)`,
			newUUID(), sess.ID, seq, "m", 1, 1, 0.0, 1)
		return err
	}
	if err := insert(1); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if err := insert(1); err == nil {
		t.Fatal("expected unique constraint violation inserting a duplicate seq")
	}
}

// TestRunMigrationRollsBackAtomicallyOnFailure is the regression guard for
// the migration runner's transaction wrapping: a migration whose 2nd
// statement fails must leave NEITHER its 1st statement's effect NOR the
// version bump committed — a partial migration previously (pre-rework, see
// git history) left the 1st statement's effect permanent while user_version
// stayed unbumped, so every later Open() replayed the same 1st statement
// against data that no longer matched, failing identically forever.
func TestRunMigrationRollsBackAtomicallyOnFailure(t *testing.T) {
	dbPath := filepath.Join(testutil.TempDir(t), "partial-migration.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(schemaSQL); err != nil {
		t.Fatalf("apply schema: %v", err)
	}

	failingMigration := func(tx *sql.Tx) error {
		if _, err := tx.Exec(`ALTER TABLE sessions ADD COLUMN scratch_col TEXT`); err != nil {
			return err
		}
		return errors.New("simulated failure on the migration's 2nd statement")
	}

	if err := runMigration(db, failingMigration, 0); err == nil {
		t.Fatal("runMigration succeeded, want an error from the simulated failure")
	}

	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if version != 0 {
		t.Errorf("user_version = %d, want 0 (unbumped — migration rolled back)", version)
	}

	rows, err := db.Query(`PRAGMA table_info(sessions)`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var dflt any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if name == "scratch_col" {
			t.Fatal("scratch_col persisted — the failed migration's 1st statement was not rolled back")
		}
	}

	// A later attempt with a corrected (all-succeeding) migration must
	// still be able to run from this same clean, unbumped starting state.
	okMigration := func(tx *sql.Tx) error {
		_, err := tx.Exec(`ALTER TABLE sessions ADD COLUMN scratch_col TEXT`)
		return err
	}
	if err := runMigration(db, okMigration, 0); err != nil {
		t.Fatalf("retry after rollback: %v", err)
	}
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read version after retry: %v", err)
	}
	if version != 1 {
		t.Errorf("user_version after successful retry = %d, want 1", version)
	}
}

func TestOpenIdempotent(t *testing.T) {
	dbPath := filepath.Join(testutil.TempDir(t), "idem.db")
	s1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 1: %v", err)
	}
	mustCreateSession(t, s1, "idem")
	s1.Close()

	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 2: %v", err)
	}
	defer s2.Close()
	if _, err := s2.GetSession("idem"); err != nil {
		t.Fatalf("reopen should preserve session: %v", err)
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

func TestApplyCompaction(t *testing.T) {
	s := newTestStore(t)
	mustCreateSession(t, s, "ac")

	for _, txt := range []string{"a", "b", "c"} {
		if err := s.AppendMessage(&Message{SessionID: "ac", Role: "user", Content: textContent(txt)}); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}

	summary := "## Big Picture\nmerged"
	if err := s.ApplyCompaction("ac", 2, summary); err != nil {
		t.Fatalf("ApplyCompaction: %v", err)
	}
	sess, err := s.GetSession("ac")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.CompactionSummary == nil || *sess.CompactionSummary != summary {
		t.Fatalf("summary = %v, want %q", sess.CompactionSummary, summary)
	}
	got, err := s.GetMessages("ac")
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(got) != 1 || got[0].Seq != 3 {
		t.Fatalf("active messages = %v, want seq 3 only", got)
	}
}

// TestGetLastCompactionBreaksCreatedAtTies reproduces a real bug: created_at
// is unix-second resolution, so two compactions recorded within the same
// second (routine — a session can legitimately auto-compact twice in a busy
// second) tied on ORDER BY created_at DESC alone, and the tie could resolve
// to either row. GetLastCompaction must always return the most recently
// RecordCompaction-ed row regardless of the created_at tie.
func TestGetLastCompactionBreaksCreatedAtTies(t *testing.T) {
	s := newTestStore(t)
	mustCreateSession(t, s, "cc")

	now := time.Now().Unix()
	if err := s.RecordCompaction(&Compaction{
		ID: "first", SessionID: "cc", Summary: "first summary", TokensBefore: 10, CreatedAt: now,
	}); err != nil {
		t.Fatalf("RecordCompaction (first): %v", err)
	}
	if err := s.RecordCompaction(&Compaction{
		ID: "second", SessionID: "cc", Summary: "second summary", TokensBefore: 20, CreatedAt: now,
	}); err != nil {
		t.Fatalf("RecordCompaction (second): %v", err)
	}

	got, err := s.GetLastCompaction("cc")
	if err != nil {
		t.Fatalf("GetLastCompaction: %v", err)
	}
	if got == nil || got.ID != "second" {
		t.Fatalf("GetLastCompaction = %+v, want the second (most recently inserted) row", got)
	}
}

// TestMessageCountsBySessionIncludesCompacted is the reported bug: a session
// that was fully compacted as its last action (every message flagged
// compacted = 1, nothing sent since) used to disappear from the count
// entirely — the session picker showed 0. Compaction never deletes a
// message, so the count must include compacted ones too.
func TestMessageCountsBySessionIncludesCompacted(t *testing.T) {
	s := newTestStore(t)
	mustCreateSession(t, s, "fully-compacted")

	for _, txt := range []string{"a", "b", "c"} {
		if err := s.AppendMessage(&Message{SessionID: "fully-compacted", Role: "user", Content: textContent(txt)}); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}
	if err := s.ApplyCompaction("fully-compacted", 3, "summary"); err != nil {
		t.Fatalf("ApplyCompaction: %v", err)
	}

	counts, err := s.MessageCountsBySession()
	if err != nil {
		t.Fatal(err)
	}
	if counts["fully-compacted"] != 3 {
		t.Fatalf("counts[fully-compacted] = %d, want 3 (compacted messages still count)", counts["fully-compacted"])
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

func ftsRowCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM messages_fts`).Scan(&n); err != nil {
		t.Fatalf("fts count: %v", err)
	}
	return n
}

func toolContent(result string) string {
	b, _ := json.Marshal([]map[string]string{
		{"type": "tool_result", "tool_use_id": "t1", "content": result},
	})
	return string(b)
}

func TestFTSSkipsToolMessages(t *testing.T) {
	s := newTestStore(t)
	mustCreateSession(t, s, "fts-tool")

	if err := s.AppendMessage(&Message{SessionID: "fts-tool", Role: "user", Content: textContent("hello user")}); err != nil {
		t.Fatalf("append user: %v", err)
	}
	if err := s.AppendMessage(&Message{SessionID: "fts-tool", Role: "assistant", Content: textContent("hello assistant")}); err != nil {
		t.Fatalf("append assistant: %v", err)
	}
	if err := s.AppendMessage(&Message{SessionID: "fts-tool", Role: "tool", Content: toolContent("tool output uniqueterm")}); err != nil {
		t.Fatalf("append tool: %v", err)
	}
	if n := ftsRowCount(t, s); n != 2 {
		t.Fatalf("fts rows = %d, want 2 (no tool indexing)", n)
	}
	res, err := s.Search("uniqueterm", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 0 {
		t.Fatalf("tool text must not be searchable, got %d results", len(res))
	}
}

func TestReconcileFTSOnOpen(t *testing.T) {
	dir := testutil.TempDir(t)
	path := filepath.Join(dir, "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	mustCreateSession(t, s, "reconcile")
	if err := s.AppendMessage(&Message{SessionID: "reconcile", Role: "tool", Content: toolContent("stale fts")}); err != nil {
		t.Fatalf("append tool: %v", err)
	}
	var toolID string
	if err := s.db.QueryRow(`SELECT id FROM messages WHERE session_id = 'reconcile' AND role = 'tool'`).Scan(&toolID); err != nil {
		t.Fatalf("tool id: %v", err)
	}
	if _, err := s.db.Exec(
		`INSERT INTO messages_fts (session_id, message_id, role, content_text) VALUES (?,?,?,?)`,
		"reconcile", toolID, "tool", "stale fts row"); err != nil {
		t.Fatalf("inject stale fts: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s.Close()
	if n := ftsRowCount(t, s); n != 0 {
		t.Fatalf("reconcileFTS left %d stale rows, want 0", n)
	}
}

func TestSetSessionTitle(t *testing.T) {
	s := newTestStore(t)
	mustCreateSession(t, s, "title-sess")

	if err := s.SetSessionTitle("title-sess", "  My Project  "); err != nil {
		t.Fatalf("SetSessionTitle: %v", err)
	}
	got, err := s.GetSession("title-sess")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.Title == nil || *got.Title != "My Project" {
		t.Fatalf("title = %v, want %q", got.Title, "My Project")
	}

	if err := s.SetSessionTitle("title-sess", ""); err != nil {
		t.Fatalf("clear title: %v", err)
	}
	got, _ = s.GetSession("title-sess")
	if got.Title != nil {
		t.Fatalf("cleared title = %v, want nil", got.Title)
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
	if err := s.ApplyCompaction("compact-search", 1, "summary"); err != nil {
		t.Fatalf("ApplyCompaction: %v", err)
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

func TestGetLastAPICallSkipsAuxiliaryCalls(t *testing.T) {
	s := newTestStore(t)
	mustCreateSession(t, s, "ctx")

	if err := s.RecordAPICall(&APICall{
		SessionID: "ctx", Seq: 1, Model: "m", InputTokens: 100, OutputTokens: 10, Cost: 0.01,
	}); err != nil {
		t.Fatalf("main call: %v", err)
	}
	if err := s.RecordAPICall(&APICall{
		SessionID: "ctx", Seq: 2, Model: "m", InputTokens: 9000, OutputTokens: 500, Cost: 0.05,
		IsCompaction: true,
	}); err != nil {
		t.Fatalf("compaction call: %v", err)
	}
	if err := s.RecordAPICall(&APICall{
		SessionID: "ctx", Seq: 3, Model: "m", InputTokens: 8000, OutputTokens: 1, Purpose: "btw",
	}); err != nil {
		t.Fatalf("btw call: %v", err)
	}
	last, err := s.GetLastAPICall("ctx")
	if err != nil {
		t.Fatalf("GetLastAPICall: %v", err)
	}
	if last.InputTokens != 100 {
		t.Fatalf("last input = %d, want 100 (auxiliary rows skipped)", last.InputTokens)
	}
}

func TestAPICallMigrationMarksMinimaxZeroInputUnknown(t *testing.T) {
	dir := testutil.TempDir(t)
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

	// Provider provenance survives the round trip so equal model names from
	// different providers cannot be mistaken for the same tokenizer/pricing.
	mustCreateSession(t, s, "provenance")
	providerCall := APICall{SessionID: "provenance", Seq: 1, Provider: "openai", Model: "shared-name", InputTokens: 1}
	if err := s.RecordAPICall(&providerCall); err != nil {
		t.Fatalf("record provider call: %v", err)
	}
	last, err = s.GetLastAPICall("provenance")
	if err != nil || last.Provider != "openai" || last.Model != "shared-name" || last.Purpose != "main" {
		t.Fatalf("provider/model/purpose round trip = %+v, err=%v", last, err)
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

// ---------- Schema versioning ----------

func TestSchemaVersionSetOnOpen(t *testing.T) {
	s := newTestStore(t)
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != len(migrations) {
		t.Fatalf("user_version = %d, want %d (len(migrations))", version, len(migrations))
	}
}

func TestSchemaVersionResumesFromStaleVersion(t *testing.T) {
	dir := testutil.TempDir(t)
	path := filepath.Join(dir, "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Simulate a database last touched by an older binary that stopped
	// applying migrations partway through.
	if _, err := s.db.Exec(`PRAGMA user_version = 0`); err != nil {
		t.Fatalf("force stale version: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s.Close()
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != len(migrations) {
		t.Fatalf("user_version after reopen = %d, want %d", version, len(migrations))
	}
}

// TestMigrationActuallyRunsSQL proves migrate() executes each step's SQL,
// not just bumps the counter: swaps in a real ALTER TABLE as a temporary
// extra migration, runs it against a stale database, and checks the column
// exists afterward.
func TestMigrationActuallyRunsSQL(t *testing.T) {
	dir := testutil.TempDir(t)
	path := filepath.Join(dir, "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.db.Exec(`PRAGMA user_version = 0`); err != nil {
		t.Fatalf("force stale version: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	orig := migrations
	migrations = append(append([]func(*sql.Tx) error{}, orig...), func(tx *sql.Tx) error {
		_, err := tx.Exec(`ALTER TABLE sessions ADD COLUMN _test_migration_marker TEXT`)
		return err
	})
	defer func() { migrations = orig }()

	s, err = Open(path)
	if err != nil {
		t.Fatalf("reopen with extra migration: %v", err)
	}
	defer s.Close()

	rows, err := s.db.Query(`PRAGMA table_info(sessions)`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var dflt any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if name == "_test_migration_marker" {
			found = true
		}
	}
	if !found {
		t.Fatal("_test_migration_marker column missing — migration step did not run")
	}

	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != len(migrations) {
		t.Fatalf("user_version = %d, want %d", version, len(migrations))
	}
}
