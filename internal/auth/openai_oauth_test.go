package auth

import (
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// withOpenAITokenURL points RefreshOpenAIToken/exchangeOpenAICode/
// postOpenAITokenForm at a local httptest server for one test, restoring
// the real URL on return — same save/defer-restore idiom as
// anthropic_oauth_test.go's withAnthropicTokenURL.
func withOpenAITokenURL(t *testing.T, u string) func() {
	t.Helper()
	orig := openaiTokenURL
	openaiTokenURL = u
	return func() { openaiTokenURL = orig }
}

// TestRefreshOpenAIToken_RequestFormExact pins the exact form-encoded body
// RefreshOpenAIToken sends — the OpenAI counterpart of the exact
// construction a real, already-fixed production bug slipped through
// untested (see refresh_crossprocess.go's doc).
func TestRefreshOpenAIToken_RequestFormExact(t *testing.T) {
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
	defer withOpenAITokenURL(t, srv.URL)()

	entry, err := RefreshOpenAIToken("input-refresh-token")
	if err != nil {
		t.Fatalf("RefreshOpenAIToken: %v", err)
	}
	if got := gotForm.Get("grant_type"); got != "refresh_token" {
		t.Errorf("grant_type = %q, want refresh_token", got)
	}
	if got := gotForm.Get("client_id"); got != openaiClientID {
		t.Errorf("client_id = %q, want %q", got, openaiClientID)
	}
	if got := gotForm.Get("refresh_token"); got != "input-refresh-token" {
		t.Errorf("refresh_token = %q, want input-refresh-token", got)
	}
	if entry.Access != "new-access" || entry.Refresh != "new-refresh" {
		t.Errorf("entry = %+v", entry)
	}
}

// TestRefreshOpenAIToken_EmptyRefreshFallsBackToInput is the OpenAI
// counterpart of TestRefreshAnthropicToken_EmptyRefreshFallsBackToInput —
// the exact "keepRefresh" bug class already fixed once.
func TestRefreshOpenAIToken_EmptyRefreshFallsBackToInput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"new-access","expires_in":3600}`))
	}))
	defer srv.Close()
	defer withOpenAITokenURL(t, srv.URL)()

	entry, err := RefreshOpenAIToken("original-refresh-token")
	if err != nil {
		t.Fatalf("RefreshOpenAIToken: %v", err)
	}
	if entry.Refresh != "original-refresh-token" {
		t.Errorf("entry.Refresh = %q, want original-refresh-token kept", entry.Refresh)
	}
}

// TestRefreshOpenAIToken_NonOKIncludesStatusAndBody confirms a failed
// refresh surfaces both the HTTP status and the server's error body.
func TestRefreshOpenAIToken_NonOKIncludesStatusAndBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer srv.Close()
	defer withOpenAITokenURL(t, srv.URL)()

	_, err := RefreshOpenAIToken("dead-refresh-token")
	if err == nil {
		t.Fatal("expected an error for a non-200 response")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("err = %q, want it to contain status 401", err.Error())
	}
	if !strings.Contains(err.Error(), "invalid_grant") {
		t.Errorf("err = %q, want it to contain the response body", err.Error())
	}
}

// TestPostOpenAITokenForm_MalformedJSONBody confirms a body that isn't even
// valid JSON surfaces a decode error instead of panicking or silently
// succeeding with a zero-value entry.
func TestPostOpenAITokenForm_MalformedJSONBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer srv.Close()
	defer withOpenAITokenURL(t, srv.URL)()

	if _, err := postOpenAITokenForm(url.Values{}, ""); err == nil {
		t.Fatal("expected a decode error for malformed JSON")
	}
}

// TestPostOpenAITokenForm_MissingAccessTokenErrors confirms
// postOpenAITokenForm actively validates access_token is present — unlike
// parseXAITokenResponse (see its own documented current-behavior test),
// this path errors rather than returning a zero-value access token.
func TestPostOpenAITokenForm_MissingAccessTokenErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"refresh_token":"r","expires_in":3600}`))
	}))
	defer srv.Close()
	defer withOpenAITokenURL(t, srv.URL)()

	_, err := postOpenAITokenForm(url.Values{}, "")
	if err == nil || !strings.Contains(err.Error(), "missing access_token") {
		t.Fatalf("err = %v, want a missing access_token error", err)
	}
}

// TestPostOpenAITokenForm_EmptyRefreshFallsBackToKeepRefresh confirms the
// keepRefresh fallback at the postOpenAITokenForm level directly (below
// RefreshOpenAIToken's own wrapper, which is covered separately above).
func TestPostOpenAITokenForm_EmptyRefreshFallsBackToKeepRefresh(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"a","expires_in":3600}`))
	}))
	defer srv.Close()
	defer withOpenAITokenURL(t, srv.URL)()

	entry, err := postOpenAITokenForm(url.Values{}, "keep-me")
	if err != nil {
		t.Fatalf("postOpenAITokenForm: %v", err)
	}
	if entry.Refresh != "keep-me" {
		t.Errorf("entry.Refresh = %q, want keep-me", entry.Refresh)
	}
}

func TestCreateState_NonEmptyHex32Chars(t *testing.T) {
	s, err := createState()
	if err != nil {
		t.Fatalf("createState: %v", err)
	}
	if len(s) != 32 {
		t.Fatalf("createState() len = %d, want 32", len(s))
	}
	if _, err := hex.DecodeString(s); err != nil {
		t.Errorf("createState() = %q, not valid hex: %v", s, err)
	}
}

func TestCreateState_TwoCallsDiffer(t *testing.T) {
	s1, err := createState()
	if err != nil {
		t.Fatalf("createState: %v", err)
	}
	s2, err := createState()
	if err != nil {
		t.Fatalf("createState: %v", err)
	}
	if s1 == s2 {
		t.Fatal("two createState calls produced the same value")
	}
}
