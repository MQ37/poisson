package auth

import (
	"net/http"
	"net/http/httptest"
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
