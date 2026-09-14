package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/orchestrator"
)

// newTestFrontend points a Frontend at an httptest server the same way
// client_test.go's newTestClient does.
func newTestFrontend(t *testing.T, srv *httptest.Server, chatID int64, allowed []string) *Frontend {
	t.Helper()
	c := newTestClient(t, srv)
	return NewFrontend(c, chatID, allowed, t.TempDir())
}

// TestFrontend_InjectedUpdateProducesExpectedCommand is the Step 25 verify
// criterion: an injected update produces exactly the expected Command on
// the output channel.
func TestFrontend_InjectedUpdateProducesExpectedCommand(t *testing.T) {
	var served int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&served, 1) == 1 {
			writeJSON(w, apiResponse{OK: true, Result: json.RawMessage(`[{
				"update_id": 100,
				"message": {
					"message_id": 5,
					"from": {"id": 111},
					"chat": {"id": -100, "is_forum": true},
					"text": "/new alpha",
					"message_thread_id": 0,
					"date": 1700000000
				}
			}]`)})
			return
		}
		// Every subsequent poll: no more updates, block the test from
		// spinning by returning nothing until ctx is cancelled.
		writeJSON(w, apiResponse{OK: true, Result: json.RawMessage(`[]`)})
	}))
	defer srv.Close()

	f := newTestFrontend(t, srv, -100, []string{"111"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := make(chan orchestrator.Command, 4)
	runDone := make(chan error, 1)
	go func() { runDone <- f.Run(ctx, out) }()

	select {
	case cmd := <-out:
		if cmd.Kind != orchestrator.CmdNew || cmd.UserID != "111" || len(cmd.Args) != 1 || cmd.Args[0] != "alpha" {
			t.Fatalf("got %+v, want CmdNew from user 111 with args [alpha]", cmd)
		}
		if cmd.Key.Frontend != "telegram" || cmd.Key.Chat != "-100" {
			t.Fatalf("got key %+v, want telegram/-100", cmd.Key)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the command")
	}

	cancel()
	<-runDone
}

// TestFrontend_UnauthorizedUserSilentlyIgnored checks a sender not on the
// allow-list produces no Command and no reply.
func TestFrontend_UnauthorizedUserSilentlyIgnored(t *testing.T) {
	var served int32
	var sendCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			atomic.AddInt32(&sendCalls, 1)
			writeJSON(w, apiResponse{OK: true, Result: json.RawMessage(`{"message_id":1}`)})
			return
		}
		if atomic.AddInt32(&served, 1) == 1 {
			writeJSON(w, apiResponse{OK: true, Result: json.RawMessage(`[{
				"update_id": 1,
				"message": {"message_id":1,"from":{"id":999},"chat":{"id":-100},"text":"hello","message_thread_id":0,"date":1700000000}
			}]`)})
			return
		}
		writeJSON(w, apiResponse{OK: true, Result: json.RawMessage(`[]`)})
	}))
	defer srv.Close()

	f := newTestFrontend(t, srv, -100, []string{"111"}) // 999 not allowed
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan orchestrator.Command, 4)
	runDone := make(chan error, 1)
	go func() { runDone <- f.Run(ctx, out) }()

	select {
	case cmd := <-out:
		t.Fatalf("unexpected command from unauthorized user: %+v", cmd)
	case <-time.After(200 * time.Millisecond):
	}
	if atomic.LoadInt32(&sendCalls) != 0 {
		t.Error("unauthorized user should get no reply at all")
	}
	cancel()
	<-runDone
}

// TestFrontend_DroppedConnectionDoesNotAdvanceOffset is the Step 25 verify
// criterion: a simulated dropped connection mid-poll does NOT advance the
// persisted offset.
func TestFrontend_DroppedConnectionDoesNotAdvanceOffset(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			// Simulate a dropped connection: close without writing a
			// response at all.
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("ResponseWriter does not support hijacking")
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Fatal(err)
			}
			conn.Close()
			return
		}
		writeJSON(w, apiResponse{OK: true, Result: json.RawMessage(`[]`)})
	}))
	defer srv.Close()

	dir := t.TempDir()
	c := newTestClient(t, srv)
	f := NewFrontend(c, -100, []string{"111"}, dir)

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- f.Run(ctx, out(t)) }()

	// Give it time to hit the dropped connection and retry at least once.
	time.Sleep(300 * time.Millisecond)
	cancel()
	<-runDone

	if _, err := os.Stat(filepath.Join(dir, "telegram_offset")); !os.IsNotExist(err) {
		t.Errorf("offset file exists after a dropped connection with zero real updates ever processed: err=%v", err)
	}
}

func out(t *testing.T) chan orchestrator.Command {
	t.Helper()
	return make(chan orchestrator.Command, 4)
}

func TestFrontend_OffsetPersistsAndSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	var served int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if atomic.AddInt32(&served, 1) == 1 {
			if body["offset"] != nil {
				t.Errorf("first call should have no offset (or 0), got %v", body["offset"])
			}
			writeJSON(w, apiResponse{OK: true, Result: json.RawMessage(`[{
				"update_id": 42,
				"message": {"message_id":1,"from":{"id":111},"chat":{"id":-100},"text":"/list","message_thread_id":0,"date":1700000000}
			}]`)})
			return
		}
		if body["offset"] != float64(43) {
			t.Errorf("subsequent call offset = %v, want 43", body["offset"])
		}
		writeJSON(w, apiResponse{OK: true, Result: json.RawMessage(`[]`)})
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	f := NewFrontend(c, -100, []string{"111"}, dir)
	ctx, cancel := context.WithCancel(context.Background())
	cmds := make(chan orchestrator.Command, 4)
	runDone := make(chan error, 1)
	go func() { runDone <- f.Run(ctx, cmds) }()

	select {
	case <-cmds:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the command")
	}
	time.Sleep(100 * time.Millisecond) // let the second poll (offset=43 check) happen
	cancel()
	<-runDone

	data, err := os.ReadFile(filepath.Join(dir, "telegram_offset"))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	if got != 43 {
		t.Errorf("persisted offset = %d, want 43", got)
	}

	// A fresh Frontend against the same dir picks up the persisted offset.
	f2 := NewFrontend(c, -100, []string{"111"}, dir)
	if f2.offset != 43 {
		t.Errorf("f2.offset = %d, want 43 (loaded from disk)", f2.offset)
	}
}
