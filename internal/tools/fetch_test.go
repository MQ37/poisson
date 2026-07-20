package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/config"
)

// TestFetch_DirectModeConvertsHTMLToMarkdown is the reported gap: fetch used
// to be registered only when the active provider was Ollama, so every other
// provider (Anthropic, OpenAI, xAI) never had a working fetch tool at all.
// With ollamaBaseURL empty, Execute must fetch the URL itself and convert
// HTML responses to Markdown via the hand-rolled converter.
func TestFetch_DirectModeConvertsHTMLToMarkdown(t *testing.T) {
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
