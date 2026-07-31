package mcpclient

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCallTool_JSONResponse covers a server that answers plain
// application/json (no SSE) with a normal tool result.
func TestCallTool_JSONResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"hello"}]}}`)
	}))
	defer srv.Close()

	res, err := CallTool(context.Background(), srv.URL, "echo", map[string]any{"q": "hi"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.Text != "hello" || res.IsError {
		t.Fatalf("res = %+v, want text=hello isError=false", res)
	}
}

// TestCallTool_SSEResponse covers Streamable HTTP's other allowed shape: a
// single "message" event carrying the JSON-RPC reply as its data — what
// Firecrawl's keyless endpoint actually returns.
func TestCallTool_SSEResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message\n")
		fmt.Fprint(w, `data: {"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"from sse"}]}}`+"\n\n")
	}))
	defer srv.Close()

	res, err := CallTool(context.Background(), srv.URL, "search", map[string]any{"query": "go"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.Text != "from sse" {
		t.Fatalf("res.Text = %q, want %q", res.Text, "from sse")
	}
}

// TestCallTool_ToolLevelError covers isError: true — the call succeeded at
// the transport/protocol level but the tool itself reported failure.
func TestCallTool_ToolLevelError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"boom"}],"isError":true}}`)
	}))
	defer srv.Close()

	res, err := CallTool(context.Background(), srv.URL, "scrape", nil)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError || res.Text != "boom" {
		t.Fatalf("res = %+v, want isError=true text=boom", res)
	}
}

// TestCallTool_ProtocolError covers a JSON-RPC-level error object (bad
// method, bad params) as opposed to a tool-level isError.
func TestCallTool_ProtocolError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"bad request"}}`)
	}))
	defer srv.Close()

	_, err := CallTool(context.Background(), srv.URL, "scrape", nil)
	if err == nil || !strings.Contains(err.Error(), "bad request") {
		t.Fatalf("err = %v, want it to surface the rpc error message", err)
	}
}

// TestCallTool_HTTPError covers a non-200 transport failure (rate limit,
// server outage) — must surface, not be treated as an empty result.
func TestCallTool_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, "rate limited")
	}))
	defer srv.Close()

	_, err := CallTool(context.Background(), srv.URL, "scrape", nil)
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("err = %v, want it to mention HTTP 429", err)
	}
}

// TestCallTool_NoHandshakeNeeded documents the core simplification this
// client relies on: a single tools/call POST with no prior initialize and no
// session ID must succeed — matching how Firecrawl's keyless tier actually
// behaves (probed live; see the package doc).
func TestCallTool_NoHandshakeNeeded(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Mcp-Session-Id") != "" {
			t.Errorf("client sent a session ID, want none")
		}
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"ok"}]}}`)
	}))
	defer srv.Close()

	if _, err := CallTool(context.Background(), srv.URL, "scrape", nil); err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if calls != 1 {
		t.Fatalf("server saw %d requests, want exactly 1 (no handshake round trip)", calls)
	}
}
