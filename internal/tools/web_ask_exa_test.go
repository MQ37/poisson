package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// isolateExaHome points os.UserHomeDir() (via $HOME, which it honors on
// unix — see os.UserHomeDir's own doc comment) at a fresh t.TempDir() for
// the duration of one test. getExaToken/issueExaToken hardcode their cache
// path as filepath.Join(home, ".poisson", "exa-token.json") with no
// separate injectable override, but they resolve "home" through
// os.UserHomeDir(), so redirecting $HOME is a real, non-hacky override — not
// a testability blocker — and keeps every test in this file from ever
// touching the real ~/.poisson/exa-token.json.
func isolateExaHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

// swapExaURLs points exaTokenURL/exaSearchURL at local httptest servers for
// one test, restoring the real URLs via the returned func (call via defer).
func swapExaURLs(t *testing.T, tokenURL, searchURL string) {
	t.Helper()
	origToken, origSearch := exaTokenURL, exaSearchURL
	if tokenURL != "" {
		exaTokenURL = tokenURL
	}
	if searchURL != "" {
		exaSearchURL = searchURL
	}
	t.Cleanup(func() {
		exaTokenURL = origToken
		exaSearchURL = origSearch
	})
}

// TestDoExaSearch_Success200: a plain 200 response is returned as-is (the
// raw exa.ai JSON, unmodified) with no error.
func TestDoExaSearch_Success200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok-abc" {
			t.Errorf("Authorization = %q, want %q", got, "Bearer tok-abc")
		}
		w.Write([]byte(`{"results":[{"title":"hello world"}]}`))
	}))
	defer srv.Close()
	swapExaURLs(t, "", srv.URL)

	result, exaErr := doExaSearch(context.Background(), "q", "tok-abc", 5, "keyword", false)
	if exaErr != nil {
		t.Fatalf("doExaSearch error: %v", exaErr)
	}
	if !strings.Contains(result, "hello world") {
		t.Errorf("result = %q, want it to contain the search result", result)
	}
}

// TestExecExaSearch_401TriggersReissueThenRetrySucceeds: the first search
// attempt gets a 401, execExaSearch must re-issue the token (hitting
// exaTokenURL a second time) and retry the search, which then succeeds with
// the freshly issued token.
func TestExecExaSearch_401TriggersReissueThenRetrySucceeds(t *testing.T) {
	isolateExaHome(t)

	var tokenCalls int32
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&tokenCalls, 1)
		json.NewEncoder(w).Encode(map[string]any{
			"token":     "tok" + string(rune('0'+n)),
			"expiresAt": 9999999999999,
		})
	}))
	defer tokenSrv.Close()

	var searchCalls int32
	searchSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&searchCalls, 1)
		if n == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"expired"}`))
			return
		}
		if got, want := r.Header.Get("Authorization"), "Bearer tok2"; got != want {
			t.Errorf("second search Authorization = %q, want %q (the re-issued token)", got, want)
		}
		w.Write([]byte(`{"results":[{"title":"second try worked"}]}`))
	}))
	defer searchSrv.Close()

	swapExaURLs(t, tokenSrv.URL, searchSrv.URL)

	result, err := execExaSearch(context.Background(), "q", 5, "keyword", false)
	if err != nil {
		t.Fatalf("execExaSearch error: %v", err)
	}
	if !strings.Contains(result, "second try worked") {
		t.Errorf("result = %q, want the retry's successful body", result)
	}
	if got := atomic.LoadInt32(&tokenCalls); got != 2 {
		t.Errorf("tokenCalls = %d, want 2 (initial issue + re-issue after 401)", got)
	}
	if got := atomic.LoadInt32(&searchCalls); got != 2 {
		t.Errorf("searchCalls = %d, want 2 (first 401 + retry)", got)
	}
}

// TestExecExaSearch_429FriendlyRateLimitMessage: a 429 from the search
// endpoint (not preceded by a 401) must surface execExaSearch's own
// friendly rate-limit message, not the raw exa.ai HTTP error.
func TestExecExaSearch_429FriendlyRateLimitMessage(t *testing.T) {
	isolateExaHome(t)

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"token": "tok1", "expiresAt": 9999999999999})
	}))
	defer tokenSrv.Close()

	var searchCalls int32
	searchSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&searchCalls, 1)
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer searchSrv.Close()

	swapExaURLs(t, tokenSrv.URL, searchSrv.URL)

	_, err := execExaSearch(context.Background(), "q", 5, "keyword", false)
	if err == nil {
		t.Fatalf("err = nil, want a rate-limit error")
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("err = %q, want it to contain %q", err.Error(), "rate limited")
	}
	// 429 isn't 401, so no re-issue/retry should happen: exactly one search call.
	if got := atomic.LoadInt32(&searchCalls); got != 1 {
		t.Errorf("searchCalls = %d, want 1 (no retry on 429)", got)
	}
}

// TestIssueExaToken_EmptyTokenFieldIsExplicitError: a 200 response whose
// JSON body parses fine but carries no (or an empty) token must fail loudly
// with a "no token"-shaped error — never silently return an empty token
// that a caller would then send onward as `Authorization: Bearer `.
func TestIssueExaToken_EmptyTokenFieldIsExplicitError(t *testing.T) {
	isolateExaHome(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"token":"","expiresAt":123}`))
	}))
	defer srv.Close()
	swapExaURLs(t, srv.URL, "")

	token, err := issueExaToken(context.Background())
	if err == nil {
		t.Fatalf("err = nil, want a \"no token\" error")
	}
	if !strings.Contains(err.Error(), "no token") {
		t.Errorf("err = %q, want it to contain %q", err.Error(), "no token")
	}
	if token != "" {
		t.Errorf("token = %q, want empty on error", token)
	}

	// Must not have cached anything on this error path.
	home, _ := os.UserHomeDir()
	if _, statErr := os.Stat(filepath.Join(home, ".poisson", "exa-token.json")); statErr == nil {
		t.Errorf("cache file written despite an error response")
	}
}

// TestIssueExaToken_MalformedJSONIsError: a response body that isn't valid
// JSON at all must also fail (decode error), not silently return an empty
// token.
func TestIssueExaToken_MalformedJSONIsError(t *testing.T) {
	isolateExaHome(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`not json at all`))
	}))
	defer srv.Close()
	swapExaURLs(t, srv.URL, "")

	token, err := issueExaToken(context.Background())
	if err == nil {
		t.Fatalf("err = nil, want a decode error for malformed JSON")
	}
	if token != "" {
		t.Errorf("token = %q, want empty on error", token)
	}
}
