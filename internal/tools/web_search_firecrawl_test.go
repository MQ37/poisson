package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// swapFirecrawlMCPURL points firecrawlMCPURL at an httptest server for the
// duration of one test, restoring it on cleanup.
func swapFirecrawlMCPURL(t *testing.T, url string) func() {
	orig := firecrawlMCPURL
	firecrawlMCPURL = url
	return func() { firecrawlMCPURL = orig }
}

// TestWebSearch_FirecrawlProviderReturnsToolText covers the happy path
// through the public Execute entry point: provider=firecrawl calls the MCP
// server's firecrawl_search tool and returns its text content verbatim.
func TestWebSearch_FirecrawlProviderReturnsToolText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.Params.Name != "firecrawl_search" {
			t.Errorf("tool name = %q, want firecrawl_search", req.Params.Name)
		}
		if req.Params.Arguments["query"] != "go slices" {
			t.Errorf("query = %v, want %q", req.Params.Arguments["query"], "go slices")
		}
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{\"data\":{\"web\":[]}}"}]}}`))
	}))
	defer srv.Close()
	defer swapFirecrawlMCPURL(t, srv.URL)()

	tool := NewWebSearchTool(nil)
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"query": "go slices", "provider": "firecrawl",
	}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if res.Content != `{"data":{"web":[]}}` {
		t.Errorf("content = %q, want the tool's text passed through", res.Content)
	}
}

// TestWebSearch_FirecrawlProviderSurfacesToolError covers isError: true from
// the MCP server (e.g. rate limited) — must reach the caller as a tool
// error, not a silently empty result.
func TestWebSearch_FirecrawlProviderSurfacesToolError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"rate limited"}],"isError":true}}`))
	}))
	defer srv.Close()
	defer swapFirecrawlMCPURL(t, srv.URL)()

	res, err := NewWebSearchTool(nil).Execute(context.Background(), mustJSON(t, map[string]any{
		"query": "q", "provider": "firecrawl",
	}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(res.Error, "rate limited") {
		t.Fatalf("error = %q, want it to surface the tool's isError text", res.Error)
	}
}

// TestFetch_FirecrawlProviderReturnsMarkdown covers fetch's firecrawl
// backend: it calls firecrawl_scrape and extracts the "markdown" field from
// the tool's JSON text.
func TestFetch_FirecrawlProviderReturnsMarkdown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.Params.Name != "firecrawl_scrape" {
			t.Errorf("tool name = %q, want firecrawl_scrape", req.Params.Name)
		}
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{\"markdown\":\"# Title\\n\\nBody.\"}"}]}}`))
	}))
	defer srv.Close()
	defer swapFirecrawlMCPURL(t, srv.URL)()

	res, err := NewFetchTool("", nil).Execute(context.Background(), mustJSON(t, map[string]any{
		"url": "https://example.com", "provider": "firecrawl",
	}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if res.Content != "# Title\n\nBody." {
		t.Errorf("content = %q, want the extracted markdown field", res.Content)
	}
}

// TestFetch_FirecrawlProviderFallsBackOnUnparsableText covers a response
// shape change or non-JSON text: must hand back the raw text rather than an
// empty string or a decode error.
func TestFetch_FirecrawlProviderFallsBackOnUnparsableText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"not json"}]}}`))
	}))
	defer srv.Close()
	defer swapFirecrawlMCPURL(t, srv.URL)()

	res, err := NewFetchTool("", nil).Execute(context.Background(), mustJSON(t, map[string]any{
		"url": "https://example.com", "provider": "firecrawl",
	}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Content != "not json" {
		t.Errorf("content = %q, want the raw tool text as fallback", res.Content)
	}
}
