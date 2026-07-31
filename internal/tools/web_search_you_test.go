package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func swapYouSearchURL(t *testing.T, url string) func() {
	orig := youSearchURL
	youSearchURL = url
	return func() { youSearchURL = orig }
}

// TestWebSearch_YouProviderReturnsBody covers the happy path through the
// public Execute entry point: provider=you hits the keyless endpoint and
// returns the response verbatim.
func TestWebSearch_YouProviderReturnsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("query"); got != "go slices" {
			t.Errorf("query = %q, want %q", got, "go slices")
		}
		w.Write([]byte(`{"results":{"web":[],"news":[]}}`))
	}))
	defer srv.Close()
	defer swapYouSearchURL(t, srv.URL)()

	res, err := NewWebSearchTool(nil).Execute(context.Background(), mustJSON(t, map[string]any{
		"query": "go slices", "provider": "you",
	}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if res.Content != `{"results":{"web":[],"news":[]}}` {
		t.Errorf("content = %q, want you.com's body passed through", res.Content)
	}
}

// TestWebSearch_YouProviderRateLimit covers the 429 case: must surface a
// clear, actionable error.
func TestWebSearch_YouProviderRateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	defer swapYouSearchURL(t, srv.URL)()

	res, err := NewWebSearchTool(nil).Execute(context.Background(), mustJSON(t, map[string]any{
		"query": "q", "provider": "you",
	}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(res.Error, "daily limit") {
		t.Fatalf("error = %q, want a rate-limit message", res.Error)
	}
}

// TestWebSearch_UnknownProviderMentionsNewProviders keeps the error message
// in sync with the added providers.
func TestWebSearch_UnknownProviderMentionsNewProviders(t *testing.T) {
	res, _ := NewWebSearchTool(nil).Execute(context.Background(), mustJSON(t, map[string]any{
		"query": "q", "provider": "bing",
	}))
	if !strings.Contains(res.Error, "firecrawl") || !strings.Contains(res.Error, "you") {
		t.Fatalf("error = %q, want it to mention firecrawl and you as valid providers", res.Error)
	}
}
