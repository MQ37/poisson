package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/store"
)

func seedSessionWithMessages(t *testing.T, st *store.Store, id string, n int) {
	t.Helper()
	if err := st.CreateSession(&store.Session{ID: id, Cwd: "/tmp", Provider: "p", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		content, _ := json.Marshal([]map[string]string{{"type": "text", "text": "msg " + string(rune('0'+i))}})
		if err := st.AppendMessage(&store.Message{SessionID: id, Role: role, Content: string(content)}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReadMessagesTool_NoStoreConfigured(t *testing.T) {
	tool := NewReadMessagesTool(nil)
	res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]any{"id": "s-x"}))
	if res.Error == "" {
		t.Fatal("expected an error when no store is configured")
	}
}

func TestReadMessagesTool_MissingID(t *testing.T) {
	st := openTestStore(t)
	tool := NewReadMessagesTool(st)
	res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]any{}))
	if res.Error == "" {
		t.Fatal("expected an error when id is missing")
	}
}

func TestReadMessagesTool_UnknownSession(t *testing.T) {
	st := openTestStore(t)
	tool := NewReadMessagesTool(st)
	res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]any{"id": "s-nope"}))
	if res.Error == "" || !strings.Contains(res.Error, "no session") {
		t.Fatalf("error = %q, want a clear 'no session' message", res.Error)
	}
}

func TestReadMessagesTool_ReturnsMessagesOldestFirst(t *testing.T) {
	st := openTestStore(t)
	seedSessionWithMessages(t, st, "s-conv", 3)

	tool := NewReadMessagesTool(st)
	res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]any{"id": "s-conv"}))
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	var entries []messageEntry
	if err := json.Unmarshal([]byte(res.Content), &entries); err != nil {
		t.Fatalf("unmarshal: %v (content=%q)", err, res.Content)
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %+v, want 3", entries)
	}
	if entries[0].Role != "user" || entries[1].Role != "assistant" || entries[2].Role != "user" {
		t.Errorf("roles out of order: %+v", entries)
	}
	if entries[0].Seq != 1 || entries[2].Seq != 3 {
		t.Errorf("seq out of order: %+v", entries)
	}
	if entries[0].Content != "msg 0" {
		t.Errorf("Content = %q, want rendered text block", entries[0].Content)
	}
}

func TestReadMessagesTool_LimitAndOffset(t *testing.T) {
	st := openTestStore(t)
	seedSessionWithMessages(t, st, "s-conv", 5)

	tool := NewReadMessagesTool(st)
	res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]any{"id": "s-conv", "limit": 2, "offset": 1}))
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	var entries []messageEntry
	if err := json.Unmarshal([]byte(res.Content), &entries); err != nil {
		t.Fatalf("unmarshal: %v (content=%q)", err, res.Content)
	}
	if len(entries) != 2 || entries[0].Seq != 2 || entries[1].Seq != 3 {
		t.Fatalf("entries = %+v, want seq 2,3 (offset 1, limit 2)", entries)
	}
}

func TestReadMessagesTool_OffsetBeyondEnd(t *testing.T) {
	st := openTestStore(t)
	seedSessionWithMessages(t, st, "s-conv", 2)

	tool := NewReadMessagesTool(st)
	res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]any{"id": "s-conv", "offset": 10}))
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if res.Content != "no messages found" {
		t.Errorf("Content = %q, want the empty-result message", res.Content)
	}
}

func TestReadMessagesTool_IncludesCompactedTurns(t *testing.T) {
	st := openTestStore(t)
	if err := st.CreateSession(&store.Session{ID: "s-compacted", Cwd: "/tmp", Provider: "p", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	content, _ := json.Marshal([]map[string]string{{"type": "text", "text": "old turn"}})
	if err := st.AppendMessage(&store.Message{SessionID: "s-compacted", Role: "user", Content: string(content), Compacted: true}); err != nil {
		t.Fatal(err)
	}

	tool := NewReadMessagesTool(st)
	res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]any{"id": "s-compacted"}))
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	var entries []messageEntry
	if err := json.Unmarshal([]byte(res.Content), &entries); err != nil {
		t.Fatalf("unmarshal: %v (content=%q)", err, res.Content)
	}
	if len(entries) != 1 || !entries[0].Compacted || entries[0].Content != "old turn" {
		t.Fatalf("entries = %+v, want the compacted turn included", entries)
	}
}

func TestRenderMessageContent_ToolBlocks(t *testing.T) {
	content := `[
		{"type":"text","text":"hello"},
		{"type":"tool_use","tool_name":"bash","tool_input":{"command":"ls"}},
		{"type":"tool_result","tool_result":"output here"},
		{"type":"thinking","thinking":"pondering"}
	]`
	got := renderMessageContent(content)
	for _, want := range []string{"hello", "bash", "output here", "pondering"} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered = %q, want it to contain %q", got, want)
		}
	}
}

func TestRenderMessageContent_FallsBackOnUnparsable(t *testing.T) {
	got := renderMessageContent("not json at all")
	if got != "not json at all" {
		t.Errorf("got = %q, want raw fallback", got)
	}
}
