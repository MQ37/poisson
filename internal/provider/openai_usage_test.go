package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/auth"
	"github.com/mq37/poisson/internal/config"
)

// realCodexUsageResponseShape is a real MITM-captured response body from
// GET /backend-api/wham/usage (Codex CLI's own usage-indicator endpoint).
const realCodexUsageResponseShape = `{
  "plan_type": "team",
  "rate_limit": {
    "allowed": true,
    "primary_window": {"used_percent": 0, "limit_window_seconds": 604800, "reset_after_seconds": 604800, "reset_at": 1784725723}
  },
  "credits": {"has_credits": false, "unlimited": false},
  "rate_limit_reset_credits": {"available_count": 2}
}`

func newTestOpenAIProvider(t *testing.T, srv *httptest.Server) *OpenAIProvider {
	t.Helper()
	p := NewOpenAIProvider(auth.AuthStore{"openai": {Type: "oauth", Access: fakeJWT(t, "acc_1"), Expires: 1 << 62}}, &config.Config{})
	p.webBaseURL = srv.URL
	return p
}

func TestCodexUsageLimits_ParsesRealShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/wham/usage" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("chatgpt-account-id"); got != "acc_1" {
			t.Errorf("chatgpt-account-id = %q", got)
		}
		if got := r.Header.Get("Authorization"); got == "" {
			t.Error("missing Authorization header")
		}
		w.Write([]byte(realCodexUsageResponseShape))
	}))
	defer srv.Close()

	p := newTestOpenAIProvider(t, srv)
	u, err := p.UsageLimits(context.Background())
	if err != nil {
		t.Fatalf("UsageLimits: %v", err)
	}
	if u.UsedPercent != 0 {
		t.Errorf("UsedPercent = %v, want 0", u.UsedPercent)
	}
	if u.ResetAfterSeconds != 604800 {
		t.Errorf("ResetAfterSeconds = %v, want 604800", u.ResetAfterSeconds)
	}
	if u.ResetCreditsAvailable != 2 {
		t.Errorf("ResetCreditsAvailable = %v, want 2", u.ResetCreditsAvailable)
	}
	if u.ResetAt.Unix() != 1784725723 {
		t.Errorf("ResetAt = %v, want unix 1784725723", u.ResetAt)
	}
}

// TestCodexUsageLimits_TTLSkipsSecondFetch mirrors
// TestAnthropicUsageLimits_TTLSkipsSecondFetch exactly — confirms the same
// shared usageTTL constant actually gates the OpenAI path too, not just
// Anthropic's.
func TestCodexUsageLimits_TTLSkipsSecondFetch(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(realCodexUsageResponseShape))
	}))
	defer srv.Close()

	p := newTestOpenAIProvider(t, srv)
	if _, err := p.UsageLimits(context.Background()); err != nil {
		t.Fatalf("first UsageLimits: %v", err)
	}
	if _, err := p.UsageLimits(context.Background()); err != nil {
		t.Fatalf("second UsageLimits: %v", err)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("server hits = %d, want 1 (second call should have used the cache)", got)
	}
}

// TestCodexUsageLimits_ForceRefreshBypassesTTL mirrors
// TestAnthropicUsageLimits_ForceRefreshBypassesTTL — confirms ForceUsageRefresh
// bypasses the TTL for the OpenAI/Codex path too.
func TestCodexUsageLimits_ForceRefreshBypassesTTL(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(realCodexUsageResponseShape))
	}))
	defer srv.Close()

	p := newTestOpenAIProvider(t, srv)
	p.usageCache = &CodexUsage{UsedPercent: 1, FetchedAt: time.Now()}

	if _, err := p.ForceUsageRefresh(context.Background()); err != nil {
		t.Fatalf("ForceUsageRefresh: %v", err)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("server hits = %d, want 1 (ForceUsageRefresh must bypass the TTL)", got)
	}
}

func TestCodexUsageLimits_ExpiredTTLRefetches(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(realCodexUsageResponseShape))
	}))
	defer srv.Close()

	p := newTestOpenAIProvider(t, srv)
	p.usageCache = &CodexUsage{FetchedAt: time.Now().Add(-6 * time.Minute)}

	if _, err := p.UsageLimits(context.Background()); err != nil {
		t.Fatalf("UsageLimits: %v", err)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("server hits = %d, want 1 (expired cache should have refetched)", got)
	}
}

func TestCodexUsageLimits_StaleCacheOnFetchError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := newTestOpenAIProvider(t, srv)
	want := &CodexUsage{UsedPercent: 42, FetchedAt: time.Now().Add(-6 * time.Minute)}
	p.usageCache = want

	got, err := p.UsageLimits(context.Background())
	if err != nil {
		t.Fatalf("UsageLimits: %v", err)
	}
	if got != want {
		t.Fatalf("got %+v, want the stale cache %+v", got, want)
	}
}

func TestCodexUsageLimits_CachedUsageLimitsNoNetworkCall(t *testing.T) {
	p := NewOpenAIProvider(auth.AuthStore{"openai": {Type: "oauth", Access: "t", Expires: 1 << 62}}, &config.Config{})
	if got := p.CachedUsageLimits(); got != nil {
		t.Fatalf("expected nil before any fetch, got %+v", got)
	}
	want := &CodexUsage{UsedPercent: 7}
	p.usageCache = want
	if got := p.CachedUsageLimits(); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// TestCodexResetUsage_ConsumesFirstAvailableCredit reproduces the exact
// 3-call flow confirmed by a real MITM capture: list credits, POST consume
// with a fresh redeem_request_id + the chosen credit id, then re-list to
// report the remaining count.
func TestCodexResetUsage_ConsumesFirstAvailableCredit(t *testing.T) {
	listCalls := 0
	var consumedBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/backend-api/wham/rate-limit-reset-credits":
			listCalls++
			if listCalls == 1 {
				w.Write([]byte(`{"credits":[
					{"id":"cred_redeemed","status":"redeemed"},
					{"id":"cred_available","status":"available"}
				],"available_count":1}`))
			} else {
				w.Write([]byte(`{"credits":[{"id":"cred_redeemed","status":"redeemed"}],"available_count":0}`))
			}
		case r.Method == "POST" && r.URL.Path == "/backend-api/wham/rate-limit-reset-credits/consume":
			json.NewDecoder(r.Body).Decode(&consumedBody)
			w.Write([]byte(`{"code":"reset","windows_reset":1}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	p := newTestOpenAIProvider(t, srv)
	p.usageCache = &CodexUsage{UsedPercent: 99} // must be dropped by a successful reset

	result, err := p.ResetUsage(context.Background())
	if err != nil {
		t.Fatalf("ResetUsage: %v", err)
	}
	if result.WindowsReset != 1 {
		t.Errorf("WindowsReset = %d, want 1", result.WindowsReset)
	}
	if result.CreditsRemaining != 0 {
		t.Errorf("CreditsRemaining = %d, want 0", result.CreditsRemaining)
	}
	if consumedBody["credit_id"] != "cred_available" {
		t.Errorf("consumed credit_id = %q, want cred_available (the redeemed one must be skipped)", consumedBody["credit_id"])
	}
	if consumedBody["redeem_request_id"] == "" {
		t.Error("missing redeem_request_id")
	}
	if p.CachedUsageLimits() != nil {
		t.Error("usage cache should be dropped after a successful reset")
	}
}

func TestCodexResetUsage_NoCreditsAvailableReturnsClearError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"credits":[{"id":"cred_1","status":"redeemed"}],"available_count":0}`))
	}))
	defer srv.Close()

	p := newTestOpenAIProvider(t, srv)
	_, err := p.ResetUsage(context.Background())
	if err == nil {
		t.Fatal("expected an error when no reset credits are available")
	}
}

func TestNewUUIDv4_LooksLikeAUUID(t *testing.T) {
	id := newUUIDv4()
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		t.Fatalf("newUUIDv4() = %q, does not look like a UUID", id)
	}
	if id[14] != '4' {
		t.Fatalf("newUUIDv4() = %q, version nibble should be 4", id)
	}
	if newUUIDv4() == id {
		t.Fatal("two calls returned the same UUID")
	}
}
