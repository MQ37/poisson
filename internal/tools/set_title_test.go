package tools

import (
	"context"
	"testing"

	"github.com/mq37/poisson/internal/store"
)

func TestSetTitleTool_NoStoreConfigured(t *testing.T) {
	tool := NewSetTitleTool(nil)
	res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]any{"title": "pr 1"}))
	if res.Error == "" {
		t.Fatal("expected an error when no store is configured")
	}
}

func TestSetTitleTool_UnboundSessionFns(t *testing.T) {
	st := openTestStore(t)
	tool := NewSetTitleTool(st)
	res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]any{"title": "pr 1"}))
	if res.Error == "" {
		t.Fatal("expected an error before SetSessionFns is called")
	}
}

func TestSetTitleTool_EmptyTitle(t *testing.T) {
	st := openTestStore(t)
	tool := NewSetTitleTool(st)
	tool.SetSessionFns(func() string { return "s-1" }, func() error { return nil })
	res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]any{"title": "   "}))
	if res.Error == "" {
		t.Fatal("expected an error for a blank title")
	}
}

func TestSetTitleTool_SetsAndRecordsHistory(t *testing.T) {
	st := openTestStore(t)
	if err := st.CreateSession(&store.Session{ID: "s-1", Cwd: "/tmp", Provider: "p", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	tool := NewSetTitleTool(st)
	tool.SetSessionFns(func() string { return "s-1" }, func() error { return nil })

	for _, title := range []string{"pr 1 draft", "pr 1 tools refactor"} {
		res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"title": title}))
		if err != nil || res.Error != "" {
			t.Fatalf("Execute(%q): err=%v res.Error=%q", title, err, res.Error)
		}
	}

	sess, err := st.GetSession("s-1")
	if err != nil {
		t.Fatal(err)
	}
	if sess.Title == nil || *sess.Title != "pr 1 tools refactor" {
		t.Fatalf("session title = %v, want %q", sess.Title, "pr 1 tools refactor")
	}
	hist, err := st.TitleHistoryForSessions([]string{"s-1"})
	if err != nil {
		t.Fatal(err)
	}
	if entries := hist["s-1"]; len(entries) != 2 || entries[0].Title != "pr 1 draft" || entries[1].Title != "pr 1 tools refactor" {
		t.Fatalf("history = %+v, want both titles oldest-first", entries)
	}
}
