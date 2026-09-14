package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestClient points a Client at an httptest server by rewriting apiBase
// via a custom RoundTripper, since apiBase is a package constant.
func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	rt := rewriteTransport{prefix: apiBase + "TESTTOKEN", target: srv.URL}
	return NewClient("TESTTOKEN", &http.Client{Transport: rt})
}

type rewriteTransport struct {
	prefix string
	target string
}

func (rt rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = "http"
	req.URL.Host = strings.TrimPrefix(rt.target, "http://")
	return http.DefaultTransport.RoundTrip(req)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func TestGetMe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/getMe") {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		writeJSON(w, apiResponse{OK: true, Result: json.RawMessage(`{"id":1,"username":"testbot"}`)})
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	u, err := c.GetMe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if u.Username != "testbot" {
		t.Fatalf("got %+v", u)
	}
}

func TestGetUpdates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["offset"] != float64(5) {
			t.Fatalf("expected offset 5, got %v", body["offset"])
		}
		writeJSON(w, apiResponse{OK: true, Result: json.RawMessage(`[{"update_id":5,"message":{"message_id":1,"text":"hi","chat":{"id":-100,"is_forum":true}}}]`)})
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	updates, err := c.GetUpdates(context.Background(), 5, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 1 || updates[0].Message.Text != "hi" {
		t.Fatalf("got %+v", updates)
	}
}

func TestSendMessage_Chunking(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if txt, _ := body["text"].(string); len(txt) > maxMsgLen {
			t.Fatalf("chunk exceeds max length: %d", len(txt))
		}
		writeJSON(w, apiResponse{OK: true, Result: json.RawMessage(`{"message_id":1}`)})
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	long := strings.Repeat("line\n", 2000) // ~10000 chars, well over 4096
	if _, err := c.SendMessage(context.Background(), -100, 0, long); err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt32(&calls); n < 2 {
		t.Fatalf("expected multiple requests for long message, got %d", n)
	}
}

func TestSendMessage_ShortSingleRequest(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		writeJSON(w, apiResponse{OK: true, Result: json.RawMessage(`{"message_id":42}`)})
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	id, err := c.SendMessage(context.Background(), -100, 42, "short text")
	if err != nil {
		t.Fatal(err)
	}
	if id != 42 {
		t.Errorf("message id = %d, want 42", id)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("expected exactly 1 request, got %d", n)
	}
}

func TestCall_RetriesOn429(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			w.WriteHeader(http.StatusOK) // Telegram returns 200 with ok:false + error_code
			writeJSON(w, apiResponse{OK: false, ErrorCode: 429, Parameters: &struct {
				RetryAfter int `json:"retry_after"`
			}{RetryAfter: 1}})
			return
		}
		writeJSON(w, apiResponse{OK: true, Result: json.RawMessage(`{"id":1,"username":"ok"}`)})
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	start := time.Now()
	u, err := c.GetMe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 1*time.Second {
		t.Fatalf("expected retry to wait for retry_after, elapsed %v", elapsed)
	}
	if u.Username != "ok" {
		t.Fatalf("got %+v", u)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("expected 2 calls (1 retry), got %d", calls)
	}
}

func TestCall_Conflict409(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, apiResponse{OK: false, ErrorCode: 409, Description: "terminated by other getUpdates request"})
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	_, err := c.GetMe(context.Background())
	if err != ErrConflict {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestForumTopicLifecycle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/createForumTopic"):
			writeJSON(w, apiResponse{OK: true, Result: json.RawMessage(`{"message_thread_id":7}`)})
		case strings.HasSuffix(r.URL.Path, "/closeForumTopic"),
			strings.HasSuffix(r.URL.Path, "/deleteForumTopic"):
			writeJSON(w, apiResponse{OK: true})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	ctx := context.Background()
	threadID, err := c.CreateForumTopic(ctx, -100, "instance-1")
	if err != nil {
		t.Fatal(err)
	}
	if threadID != 7 {
		t.Fatalf("got thread id %d", threadID)
	}
	if err := c.CloseForumTopic(ctx, -100, threadID); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteForumTopic(ctx, -100, threadID); err != nil {
		t.Fatal(err)
	}
}

func TestEditMessageText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["message_id"] != float64(9) {
			t.Fatalf("got %v", body["message_id"])
		}
		writeJSON(w, apiResponse{OK: true})
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	if err := c.EditMessageText(context.Background(), -100, 9, "updated"); err != nil {
		t.Fatal(err)
	}
}
