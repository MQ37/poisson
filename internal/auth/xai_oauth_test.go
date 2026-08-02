package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

// withXAITokenURL points RefreshXAIToken at a local httptest server for one
// test, restoring the real URL on return — same save/defer-restore idiom as
// anthropic_oauth_test.go's withAnthropicTokenURL.
func withXAITokenURL(t *testing.T, u string) func() {
	t.Helper()
	orig := xaiTokenURL
	xaiTokenURL = u
	return func() { xaiTokenURL = orig }
}

// TestRefreshXAIToken_RequestFormExact pins the exact form-encoded body
// RefreshXAIToken sends — the xAI counterpart of the exact construction a
// real, already-fixed production bug slipped through untested (see
// refresh_crossprocess.go's doc).
func TestRefreshXAIToken_RequestFormExact(t *testing.T) {
	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		gotForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`))
	}))
	defer srv.Close()
	defer withXAITokenURL(t, srv.URL)()

	entry, err := RefreshXAIToken("input-refresh-token")
	if err != nil {
		t.Fatalf("RefreshXAIToken: %v", err)
	}
	if got := gotForm.Get("grant_type"); got != "refresh_token" {
		t.Errorf("grant_type = %q, want refresh_token", got)
	}
	if got := gotForm.Get("client_id"); got != xaiClientID {
		t.Errorf("client_id = %q, want %q", got, xaiClientID)
	}
	if got := gotForm.Get("refresh_token"); got != "input-refresh-token" {
		t.Errorf("refresh_token = %q, want input-refresh-token", got)
	}
	if entry.Access != "new-access" || entry.Refresh != "new-refresh" {
		t.Errorf("entry = %+v", entry)
	}
}

// TestRefreshXAIToken_EmptyRefreshFallsBackToInput is the xAI counterpart of
// TestRefreshAnthropicToken_EmptyRefreshFallsBackToInput.
func TestRefreshXAIToken_EmptyRefreshFallsBackToInput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"new-access","expires_in":3600}`))
	}))
	defer srv.Close()
	defer withXAITokenURL(t, srv.URL)()

	entry, err := RefreshXAIToken("original-refresh-token")
	if err != nil {
		t.Fatalf("RefreshXAIToken: %v", err)
	}
	if entry.Refresh != "original-refresh-token" {
		t.Errorf("entry.Refresh = %q, want original-refresh-token kept", entry.Refresh)
	}
}

// TestRefreshXAIToken_NonOKIncludesStatusAndBody confirms a failed refresh
// surfaces both the HTTP status and the server's error body.
func TestRefreshXAIToken_NonOKIncludesStatusAndBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer srv.Close()
	defer withXAITokenURL(t, srv.URL)()

	_, err := RefreshXAIToken("dead-refresh-token")
	if err == nil {
		t.Fatal("expected an error for a non-200 response")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("err = %q, want it to contain status 400", err.Error())
	}
	if !strings.Contains(err.Error(), "invalid_grant") {
		t.Errorf("err = %q, want it to contain the response body", err.Error())
	}
}

func TestParseXAITokenResponse_WellFormed(t *testing.T) {
	body := strings.NewReader(`{"access_token":"a","refresh_token":"r","expires_in":3600}`)
	entry, err := parseXAITokenResponse(body, "keep")
	if err != nil {
		t.Fatalf("parseXAITokenResponse: %v", err)
	}
	if entry.Access != "a" || entry.Refresh != "r" || entry.Type != "oauth" {
		t.Errorf("entry = %+v", entry)
	}
}

func TestParseXAITokenResponse_MalformedJSON(t *testing.T) {
	if _, err := parseXAITokenResponse(strings.NewReader("not json"), "keep"); err == nil {
		t.Fatal("expected a decode error")
	}
}

func TestParseXAITokenResponse_EmptyRefreshFallsBackToKeepRefresh(t *testing.T) {
	entry, err := parseXAITokenResponse(strings.NewReader(`{"access_token":"a","expires_in":3600}`), "keep-me")
	if err != nil {
		t.Fatalf("parseXAITokenResponse: %v", err)
	}
	if entry.Refresh != "keep-me" {
		t.Errorf("entry.Refresh = %q, want keep-me", entry.Refresh)
	}
}

// TestParseXAITokenResponse_MissingAccessTokenCurrentBehavior documents the
// CURRENT behavior, whatever it is: unlike postOpenAITokenForm (which
// actively errors), parseXAITokenResponse does NOT validate that
// access_token is present — a response missing the field decodes
// successfully into a zero-value Access, with no error.
func TestParseXAITokenResponse_MissingAccessTokenCurrentBehavior(t *testing.T) {
	entry, err := parseXAITokenResponse(strings.NewReader(`{"refresh_token":"r","expires_in":3600}`), "")
	if err != nil {
		t.Fatalf("parseXAITokenResponse: %v (current behavior is no error here)", err)
	}
	if entry.Access != "" {
		t.Errorf("entry.Access = %q, want empty (no access_token in response)", entry.Access)
	}
}

func TestEnsureXAIFresh_NoEntryErrors(t *testing.T) {
	if _, err := EnsureXAIFresh(AuthStore{}, 5*60*1000); err == nil {
		t.Fatal("expected an error with no xai entry")
	}
}

func TestEnsureXAIFresh_WrongTypeErrors(t *testing.T) {
	store := AuthStore{"xai": {Type: "api_key", Key: "sk-x"}}
	if _, err := EnsureXAIFresh(store, 5*60*1000); err == nil {
		t.Fatal("expected an error for a non-oauth xai entry")
	}
}

// TestEnsureXAIFresh_NotExpiredReturnsUnchangedNoNetwork proves the
// proactive-refresh skew check actually gates the network call: an entry
// far from expiring must come back unchanged with zero requests reaching
// xaiTokenURL — pointed at a server that would fail the test if ever hit.
func TestEnsureXAIFresh_NotExpiredReturnsUnchangedNoNetwork(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()
	defer withXAITokenURL(t, srv.URL)()

	entry := AuthEntry{Type: "oauth", Access: "still-fresh", Refresh: "r", Expires: farFutureMs}
	store := AuthStore{"xai": entry}
	got, err := EnsureXAIFresh(store, 5*60*1000)
	if err != nil {
		t.Fatalf("EnsureXAIFresh: %v", err)
	}
	if got != entry {
		t.Errorf("got %+v, want unchanged %+v", got, entry)
	}
	if hits.Load() != 0 {
		t.Errorf("hits = %d, want 0 (no refresh should have been attempted)", hits.Load())
	}
}

// TestForceRefreshXAI_NoEntryErrors confirms ForceRefreshXAI shares
// EnsureXAIFresh's "no entry" error branch and returns before ever
// attempting a refresh (no auth.json access needed to reach this branch).
func TestForceRefreshXAI_NoEntryErrors(t *testing.T) {
	if _, err := ForceRefreshXAI(AuthStore{}); err == nil {
		t.Fatal("expected an error with no xai entry")
	}
}
