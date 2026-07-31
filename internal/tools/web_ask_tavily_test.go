package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func swapTavilySearchURL(t *testing.T, url string) func() {
	orig := tavilySearchURL
	tavilySearchURL = url
	return func() { tavilySearchURL = orig }
}

// TestWebAskTool_TavilyProviderReturnsBody covers the happy path through the
// public Execute entry point: provider=tavily hits the keyless endpoint with
// the right header and returns the response verbatim.
func TestWebAskTool_TavilyProviderReturnsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Tavily-Access-Mode") != "keyless" {
			t.Errorf("missing X-Tavily-Access-Mode: keyless header")
		}
		w.Write([]byte(`{"query":"q","answer":"the answer","results":[]}`))
	}))
	defer srv.Close()
	defer swapTavilySearchURL(t, srv.URL)()

	tool := NewWebAskTool(nil)
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"query": "q", "provider": "tavily",
	}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if res.Content != `{"query":"q","answer":"the answer","results":[]}` {
		t.Errorf("content = %q, want tavily's body passed through", res.Content)
	}
}

// TestWebAskTool_TavilyProviderRateLimit covers the 429 case: must surface a
// clear, actionable error, not a raw body dump.
func TestWebAskTool_TavilyProviderRateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	defer swapTavilySearchURL(t, srv.URL)()

	res, err := NewWebAskTool(nil).Execute(context.Background(), mustJSON(t, map[string]any{
		"query": "q", "provider": "tavily",
	}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(res.Error, "rate limit") {
		t.Fatalf("error = %q, want a rate-limit message", res.Error)
	}
}

// TestWebAskTool_UnknownProviderMentionsTavily keeps the error message in
// sync with the added provider.
func TestWebAskTool_UnknownProviderMentionsTavily(t *testing.T) {
	res, _ := NewWebAskTool(nil).Execute(context.Background(), mustJSON(t, map[string]any{
		"query": "q", "provider": "bing",
	}))
	if !strings.Contains(res.Error, "tavily") {
		t.Fatalf("error = %q, want it to mention tavily as a valid provider", res.Error)
	}
}
