package tools

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/config"
)

// allowLoopbackFetchForTest disables fetchDirect's SSRF loopback/private-IP
// block for one test, restoring it on cleanup — needed because
// httptest.NewServer always listens on 127.0.0.1.
func allowLoopbackFetchForTest(t *testing.T) {
	orig := blockedFetchIP
	blockedFetchIP = func(net.IP) bool { return false }
	t.Cleanup(func() { blockedFetchIP = orig })
}

// TestFetch_DirectModeConvertsHTMLToMarkdown is the reported gap: fetch used
// to be registered only when the active provider was Ollama, so every other
// provider (Anthropic, OpenAI, xAI) never had a working fetch tool at all.
// With ollamaBaseURL empty, Execute must fetch the URL itself and convert
// HTML responses to Markdown via the hand-rolled converter.
func TestFetch_DirectModeConvertsHTMLToMarkdown(t *testing.T) {
	allowLoopbackFetchForTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte("<h1>Title</h1><p>Hello <strong>world</strong>.</p>"))
	}))
	defer srv.Close()

	tool := NewFetchTool("")
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]string{"url": srv.URL}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("fetch error: %s", res.Error)
	}
	want := "# Title\n\nHello **world**.\n"
	if res.Content != want {
		t.Fatalf("content = %q, want %q", res.Content, want)
	}
}

// TestFetch_DirectModePassesThroughNonHTML confirms plain text/JSON responses
// are returned as-is — there's nothing to convert.
func TestFetch_DirectModePassesThroughNonHTML(t *testing.T) {
	allowLoopbackFetchForTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	tool := NewFetchTool("")
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]string{"url": srv.URL}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Content != `{"ok":true}` {
		t.Fatalf("content = %q, want raw JSON passthrough", res.Content)
	}
}

func TestFetch_DirectModeNon200ReturnsError(t *testing.T) {
	allowLoopbackFetchForTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("not found"))
	}))
	defer srv.Close()

	tool := NewFetchTool("")
	res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]string{"url": srv.URL}))
	if res.Error == "" || !strings.Contains(res.Error, "404") {
		t.Fatalf("expected a 404 error, got %q", res.Error)
	}
}

// TestFetch_DirectModeRejectsNonHTTPScheme confirms fetchDirect refuses
// file:// and similar schemes before ever making a request.
func TestFetch_DirectModeRejectsNonHTTPScheme(t *testing.T) {
	tool := NewFetchTool("")
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]string{"url": "file:///etc/passwd"}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(res.Error, "http or https") {
		t.Fatalf("expected scheme-rejection error, got %q", res.Error)
	}
}

// TestFetch_DirectModeBlocksLoopback is the regression guard for the SSRF
// fix: without allowLoopbackFetchForTest, a loopback URL (same address class
// as cloud metadata endpoints and internal-only services) must be refused,
// not silently fetched.
func TestFetch_DirectModeBlocksLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("should never be reached"))
	}))
	defer srv.Close()

	tool := NewFetchTool("")
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]string{"url": srv.URL}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Error == "" || !strings.Contains(res.Error, "internal address") {
		t.Fatalf("expected an internal-address refusal, got content=%q error=%q", res.Content, res.Error)
	}
}

// TestFetch_OllamaModeProxiesRequest confirms a non-empty ollamaBaseURL
// still routes through the existing Ollama web_fetch proxy, unchanged.
func TestFetch_OllamaModeProxiesRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/fetch" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(200)
		w.Write([]byte("extracted via ollama"))
	}))
	defer srv.Close()

	tool := NewFetchTool(srv.URL)
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]string{"url": "https://example.com"}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Content != "extracted via ollama" {
		t.Fatalf("content = %q, want ollama proxy passthrough", res.Content)
	}
}

func TestOllamaBaseURL(t *testing.T) {
	if got := OllamaBaseURL(nil); got != "http://localhost:11434" {
		t.Errorf("nil config: got %q", got)
	}
	if got := OllamaBaseURL(&config.Config{}); got != "http://localhost:11434" {
		t.Errorf("empty config: got %q", got)
	}
	cfg := &config.Config{}
	cfg.Ollama.BaseURL = "http://custom:1234"
	if got := OllamaBaseURL(cfg); got != "http://custom:1234" {
		t.Errorf("configured base URL: got %q, want http://custom:1234", got)
	}
}
