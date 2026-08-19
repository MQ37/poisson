package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/mq37/poisson/internal/store"
	"github.com/mq37/poisson/internal/testutil"
)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	dir := testutil.TempDir(t)
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestListSessionsTool_NoStoreConfigured(t *testing.T) {
	tool := NewListSessionsTool(nil)
	res, _ := tool.Execute(context.Background(), nil)
	if res.Error == "" {
		t.Fatal("expected an error when no store is configured")
	}
}

func TestListSessionsTool_Empty(t *testing.T) {
	st := openTestStore(t)
	tool := NewListSessionsTool(st)
	res, _ := tool.Execute(context.Background(), nil)
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if res.Content != "no sessions found" {
		t.Errorf("Content = %q, want the empty-list message", res.Content)
	}
}

func TestListSessionsTool_ListsWithMessageCounts(t *testing.T) {
	st := openTestStore(t)
	title := "named one"
	if err := st.CreateSession(&store.Session{ID: "s-named", Title: &title, Cwd: "/tmp", Provider: "anthropic", Model: "claude"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(&store.Session{ID: "s-untitled", Cwd: "/tmp", Provider: "anthropic", Model: "claude"}); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendMessage(&store.Message{SessionID: "s-named", Role: "user", Content: `[{"type":"text","text":"hi"}]`}); err != nil {
		t.Fatal(err)
	}

	tool := NewListSessionsTool(st)
	res, _ := tool.Execute(context.Background(), nil)
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}

	var entries []sessionListEntry
	if err := json.Unmarshal([]byte(res.Content), &entries); err != nil {
		t.Fatalf("unmarshal: %v (content=%q)", err, res.Content)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %+v, want 2", entries)
	}
	byID := map[string]sessionListEntry{}
	for _, e := range entries {
		byID[e.SessionID] = e
	}
	named, ok := byID["s-named"]
	if !ok {
		t.Fatal("missing s-named entry")
	}
	if named.Title != "named one" || named.MessageCount != 1 || named.CreatedAt == "" || named.UpdatedAt == "" {
		t.Errorf("named entry = %+v, wrong shape", named)
	}
	untitled, ok := byID["s-untitled"]
	if !ok {
		t.Fatal("missing s-untitled entry")
	}
	if untitled.Title != "" || untitled.MessageCount != 0 {
		t.Errorf("untitled entry = %+v, wrong shape", untitled)
	}
}

func TestListSessionsTool_TitleHistory(t *testing.T) {
	st := openTestStore(t)
	if err := st.CreateSession(&store.Session{ID: "s-renamed", Cwd: "/tmp", Provider: "p", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(&store.Session{ID: "s-never-renamed", Cwd: "/tmp", Provider: "p", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSessionTitle("s-renamed", "pr 1 draft"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSessionTitle("s-renamed", "pr 1 tools refactor"); err != nil {
		t.Fatal(err)
	}

	tool := NewListSessionsTool(st)
	res, _ := tool.Execute(context.Background(), nil)
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	var entries []sessionListEntry
	if err := json.Unmarshal([]byte(res.Content), &entries); err != nil {
		t.Fatalf("unmarshal: %v (content=%q)", err, res.Content)
	}
	byID := map[string]sessionListEntry{}
	for _, e := range entries {
		byID[e.SessionID] = e
	}

	renamed := byID["s-renamed"]
	if len(renamed.TitleHistory) != 2 || renamed.TitleHistory[0].Title != "pr 1 draft" || renamed.TitleHistory[1].Title != "pr 1 tools refactor" {
		t.Fatalf("titleHistory = %+v, want both titles oldest-first ending at current", renamed.TitleHistory)
	}
	if renamed.TitleHistory[0].CreatedAt == "" {
		t.Error("titleHistory entry missing createdAt")
	}

	neverRenamed := byID["s-never-renamed"]
	if neverRenamed.TitleHistory != nil {
		t.Errorf("titleHistory = %+v, want omitted for a session never renamed", neverRenamed.TitleHistory)
	}
}

func TestListSessionsTool_NamedFilter(t *testing.T) {
	st := openTestStore(t)
	title := "named one"
	if err := st.CreateSession(&store.Session{ID: "s-named", Title: &title, Cwd: "/tmp", Provider: "p", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(&store.Session{ID: "s-untitled", Cwd: "/tmp", Provider: "p", Model: "m"}); err != nil {
		t.Fatal(err)
	}

	tool := NewListSessionsTool(st)
	res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]any{"named": true}))
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	var entries []sessionListEntry
	if err := json.Unmarshal([]byte(res.Content), &entries); err != nil {
		t.Fatalf("unmarshal: %v (content=%q)", err, res.Content)
	}
	if len(entries) != 1 || entries[0].SessionID != "s-named" {
		t.Fatalf("entries = %+v, want only s-named", entries)
	}
}

// TestListSessionsTool_NamedFilterNotStarvedByLimit confirms a small limit
// combined with named=true still returns up to `limit` NAMED sessions, not
// up to `limit` sessions fetched before filtering (which could yield fewer
// than the caller asked for, or zero, if the newest rows happen to be
// untitled).
func TestListSessionsTool_NamedFilterNotStarvedByLimit(t *testing.T) {
	st := openTestStore(t)
	// Three untitled sessions created first (older / lower updated_at),
	// then one named session last (newest) — a naive "fetch limit=1 THEN
	// filter" would fetch only the newest (named) row and get lucky here,
	// so add enough untitled rows that a limit=2 fetch-then-filter would
	// come back empty unless the fix (filter-then-limit) is in place.
	for i := 0; i < 3; i++ {
		id := "s-untitled-" + string(rune('a'+i))
		if err := st.CreateSession(&store.Session{ID: id, Cwd: "/tmp", Provider: "p", Model: "m"}); err != nil {
			t.Fatal(err)
		}
	}
	title := "the named one"
	if err := st.CreateSession(&store.Session{ID: "s-named", Title: &title, Cwd: "/tmp", Provider: "p", Model: "m"}); err != nil {
		t.Fatal(err)
	}

	tool := NewListSessionsTool(st)
	res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]any{"named": true, "limit": 2}))
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	var entries []sessionListEntry
	if err := json.Unmarshal([]byte(res.Content), &entries); err != nil {
		t.Fatalf("unmarshal: %v (content=%q)", err, res.Content)
	}
	if len(entries) != 1 || entries[0].SessionID != "s-named" {
		t.Fatalf("entries = %+v, want exactly the 1 named session", entries)
	}
}
