package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPostTokenRequestSpoofsUserAgent verifies the token exchange/refresh
// request spoofs axios's User-Agent — real Claude Code's OAuth token calls
// go through its bundled axios client, not the SDK's own fetch, so a bare Go
// User-Agent on this endpoint is a fingerprintable tell even though
// /v1/messages itself is otherwise fully spoofed (see
// opencode-anthropic-auth's index.ts, same header/value).
func TestPostTokenRequestSpoofsUserAgent(t *testing.T) {
	var gotUA string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"a","refresh_token":"r","expires_in":3600}`))
	}))
	defer server.Close()

	if _, err := postTokenRequest(server.URL, map[string]string{"grant_type": "refresh_token"}, ""); err != nil {
		t.Fatalf("postTokenRequest: %v", err)
	}
	if gotUA != "axios/1.13.6" {
		t.Errorf("User-Agent = %q, want %q", gotUA, "axios/1.13.6")
	}
}

// withAnthropicTokenURL points RefreshAnthropicToken/exchangeAnthropicCode at
// a local httptest server for one test, restoring the real URL on return —
// same save/defer-restore idiom as internal/tools/web_ask_grok_test.go's
// swapGrokResponsesURL, needed because anthropicTokenURL is a shared mutable
// package var (see its own doc comment).
func withAnthropicTokenURL(t *testing.T, url string) func() {
	t.Helper()
	orig := anthropicTokenURL
	anthropicTokenURL = url
	return func() { anthropicTokenURL = orig }
}

// TestRefreshAnthropicToken_RequestBodyExact pins the exact JSON body
// RefreshAnthropicToken sends. This is the exact construction a real,
// already-fixed production bug slipped through untested (see
// refresh_crossprocess.go's doc comments for the incident) — pinning the
// wire shape here guards against a future refactor silently dropping or
// renaming a field.
func TestRefreshAnthropicToken_RequestBodyExact(t *testing.T) {
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`))
	}))
	defer srv.Close()
	defer withAnthropicTokenURL(t, srv.URL)()

	entry, err := RefreshAnthropicToken("input-refresh-token")
	if err != nil {
		t.Fatalf("RefreshAnthropicToken: %v", err)
	}
	want := map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     anthropicClientID,
		"refresh_token": "input-refresh-token",
	}
	if len(gotBody) != len(want) {
		t.Fatalf("request body = %+v, want exactly %+v", gotBody, want)
	}
	for k, v := range want {
		if gotBody[k] != v {
			t.Errorf("request body[%q] = %q, want %q", k, gotBody[k], v)
		}
	}
	if entry.Access != "new-access" || entry.Refresh != "new-refresh" {
		t.Errorf("entry = %+v", entry)
	}
}

// TestRefreshAnthropicToken_EmptyRefreshFallsBackToInput reproduces the
// exact bug class already fixed once (see refresh_crossprocess.go's doc): a
// refresh response that omits refresh_token must not blank the entry's
// refresh token — the caller must keep the ORIGINAL refresh token it sent,
// not lose it.
func TestRefreshAnthropicToken_EmptyRefreshFallsBackToInput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"new-access","expires_in":3600}`))
	}))
	defer srv.Close()
	defer withAnthropicTokenURL(t, srv.URL)()

	entry, err := RefreshAnthropicToken("original-refresh-token")
	if err != nil {
		t.Fatalf("RefreshAnthropicToken: %v", err)
	}
	if entry.Refresh != "original-refresh-token" {
		t.Errorf("entry.Refresh = %q, want the original input token kept (original-refresh-token)", entry.Refresh)
	}
	if entry.Access != "new-access" {
		t.Errorf("entry.Access = %q, want new-access", entry.Access)
	}
}

// TestRefreshAnthropicToken_NonOKIncludesStatusAndBody confirms a failed
// refresh surfaces both the HTTP status and the server's error body, not a
// generic opaque error.
func TestRefreshAnthropicToken_NonOKIncludesStatusAndBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"invalid_grant","error_description":"refresh token expired"}`))
	}))
	defer srv.Close()
	defer withAnthropicTokenURL(t, srv.URL)()

	_, err := RefreshAnthropicToken("dead-refresh-token")
	if err == nil {
		t.Fatal("expected an error for a non-200 response")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("err = %q, want it to contain the status code 400", err.Error())
	}
	if !strings.Contains(err.Error(), "refresh token expired") {
		t.Errorf("err = %q, want it to contain the response body text", err.Error())
	}
}

// TestGeneratePKCE_ValidVerifierAndChallenge recomputes the S256 challenge
// independently and checks it against generatePKCE's own output.
func TestGeneratePKCE_ValidVerifierAndChallenge(t *testing.T) {
	verifier, challenge, err := generatePKCE()
	if err != nil {
		t.Fatalf("generatePKCE: %v", err)
	}
	if verifier == "" || challenge == "" {
		t.Fatal("verifier/challenge must be non-empty")
	}
	if _, err := base64.RawURLEncoding.DecodeString(verifier); err != nil {
		t.Errorf("verifier is not valid base64url: %v", err)
	}
	sum := sha256.Sum256([]byte(verifier))
	wantChallenge := base64.RawURLEncoding.EncodeToString(sum[:])
	if challenge != wantChallenge {
		t.Errorf("challenge = %q, want SHA256(verifier) = %q", challenge, wantChallenge)
	}
}

// TestGeneratePKCE_TwoCallsDiffer confirms verifiers aren't reused/fixed.
func TestGeneratePKCE_TwoCallsDiffer(t *testing.T) {
	v1, _, err := generatePKCE()
	if err != nil {
		t.Fatalf("generatePKCE: %v", err)
	}
	v2, _, err := generatePKCE()
	if err != nil {
		t.Fatalf("generatePKCE: %v", err)
	}
	if v1 == v2 {
		t.Fatal("two generatePKCE calls produced the same verifier")
	}
}

func TestParseRedirectInput_FullCallbackURL(t *testing.T) {
	if got := parseRedirectInput("http://localhost:53692/callback?code=abc123&state=xyz"); got != "abc123" {
		t.Errorf("parseRedirectInput = %q, want abc123", got)
	}
}

func TestParseRedirectInput_RawCode(t *testing.T) {
	if got := parseRedirectInput("just-a-raw-code"); got != "just-a-raw-code" {
		t.Errorf("parseRedirectInput = %q, want just-a-raw-code", got)
	}
}

func TestParseRedirectInput_Empty(t *testing.T) {
	if got := parseRedirectInput(""); got != "" {
		t.Errorf("parseRedirectInput(\"\") = %q, want empty", got)
	}
	if got := parseRedirectInput("   "); got != "" {
		t.Errorf("parseRedirectInput(whitespace) = %q, want empty", got)
	}
}
