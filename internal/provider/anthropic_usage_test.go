package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/auth"
	"github.com/mq37/poisson/internal/config"
)

// realUsageResponseShape is a real MITM-captured response body from
// GET /api/oauth/usage (Claude Code's own usage-indicator endpoint), with
// the account-identifying fields left as-is since none of them are secrets.
const realUsageResponseShape = `{"five_hour":{"utilization":31.0,"resets_at":"2026-07-15T13:40:00.392880+00:00","limit_dollars":null,"used_dollars":null,"remaining_dollars":null},"seven_day":{"utilization":29.0,"resets_at":"2026-07-16T19:00:00.392902+00:00","limit_dollars":null,"used_dollars":null,"remaining_dollars":null},"extra_usage":{"is_enabled":true,"monthly_limit":20000,"used_credits":466.0,"utilization":2.33,"currency":"EUR","decimal_places":2,"disabled_reason":null,"daily":null,"weekly":null},"limits":[{"kind":"session","group":"session","percent":31,"severity":"normal","resets_at":"2026-07-15T13:40:00.392880+00:00","scope":null,"is_active":true}]}`

func TestAnthropicUsageLimits_ParsesRealShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/oauth/usage" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("anthropic-beta"); got != "oauth-2025-04-20" {
			t.Errorf("anthropic-beta = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer t" {
			t.Errorf("Authorization = %q", got)
		}
		w.Write([]byte(realUsageResponseShape))
	}))
	defer srv.Close()

	p := NewAnthropicProvider(auth.AuthStore{"anthropic": {Type: "oauth", Access: "t"}}, &config.Config{Stealth: config.DefaultStealthConfig()})
	p.baseURL = srv.URL

	u, err := p.UsageLimits(context.Background())
	if err != nil {
		t.Fatalf("UsageLimits: %v", err)
	}
	if u.FiveHour.UtilizationPct != 31.0 {
		t.Errorf("FiveHour.UtilizationPct = %v, want 31.0", u.FiveHour.UtilizationPct)
	}
	if u.SevenDay.UtilizationPct != 29.0 {
		t.Errorf("SevenDay.UtilizationPct = %v, want 29.0", u.SevenDay.UtilizationPct)
	}
	if !u.ExtraUsageEnabled {
		t.Error("ExtraUsageEnabled = false, want true")
	}
	// monthly_limit=20000, used_credits=466.0, decimal_places=2 -> minor units,
	// so displayed EUR amounts are those /100.
	if u.ExtraLimit != 200.0 {
		t.Errorf("ExtraLimit = %v, want 200.0", u.ExtraLimit)
	}
	if u.ExtraUsed != 4.66 {
		t.Errorf("ExtraUsed = %v, want 4.66", u.ExtraUsed)
	}
	if u.ExtraCurrency != "EUR" {
		t.Errorf("ExtraCurrency = %q, want EUR", u.ExtraCurrency)
	}
}

// TestAnthropicUsageLimits_TTLSkipsSecondFetch confirms the 5-minute TTL
// actually gates the network call: two calls in quick succession must hit
// the server only once.
func TestAnthropicUsageLimits_TTLSkipsSecondFetch(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(realUsageResponseShape))
	}))
	defer srv.Close()

	p := NewAnthropicProvider(auth.AuthStore{"anthropic": {Type: "oauth", Access: "t"}}, &config.Config{Stealth: config.DefaultStealthConfig()})
	p.baseURL = srv.URL

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

// TestAnthropicUsageLimits_ExpiredTTLRefetches confirms a cache older than
// usageTTL triggers a real fetch instead of being served stale forever.
func TestAnthropicUsageLimits_ExpiredTTLRefetches(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(realUsageResponseShape))
	}))
	defer srv.Close()

	p := NewAnthropicProvider(auth.AuthStore{"anthropic": {Type: "oauth", Access: "t"}}, &config.Config{Stealth: config.DefaultStealthConfig()})
	p.baseURL = srv.URL
	// Seed an already-expired cache entry directly (avoids waiting 5 minutes).
	p.usageCache = &AnthropicUsageLimits{FetchedAt: time.Now().Add(-6 * time.Minute)}

	if _, err := p.UsageLimits(context.Background()); err != nil {
		t.Fatalf("UsageLimits: %v", err)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("server hits = %d, want 1 (expired cache should have refetched)", got)
	}
}

// TestAnthropicUsageLimits_RequiresOAuth confirms an API-key-only auth store
// fails fast with no network call — the endpoint is OAuth-only.
func TestAnthropicUsageLimits_RequiresOAuth(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()

	p := NewAnthropicProvider(auth.AuthStore{"anthropic": {Type: "api_key", Key: "sk-ant-x"}}, &config.Config{})
	p.baseURL = srv.URL

	_, err := p.UsageLimits(context.Background())
	if err == nil {
		t.Fatal("expected an error for API-key auth")
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("server hits = %d, want 0 (should never have made a request)", got)
	}
}

// TestAnthropicUsageLimits_StaleCacheOnFetchError confirms a fetch failure
// after a cache already exists returns the stale cache instead of an error
// — a background status-bar poll shouldn't blank a number already on screen.
func TestAnthropicUsageLimits_StaleCacheOnFetchError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := NewAnthropicProvider(auth.AuthStore{"anthropic": {Type: "oauth", Access: "t"}}, &config.Config{Stealth: config.DefaultStealthConfig()})
	p.baseURL = srv.URL
	want := &AnthropicUsageLimits{FiveHour: usageWindow{UtilizationPct: 31}, FetchedAt: time.Now().Add(-6 * time.Minute)}
	p.usageCache = want

	got, err := p.UsageLimits(context.Background())
	if err != nil {
		t.Fatalf("UsageLimits: %v", err)
	}
	if got != want {
		t.Fatalf("got %+v, want the stale cache %+v", got, want)
	}
}

func TestAnthropicUsageLimits_CachedUsageLimitsNoNetworkCall(t *testing.T) {
	p := NewAnthropicProvider(auth.AuthStore{"anthropic": {Type: "oauth", Access: "t"}}, &config.Config{})
	if got := p.CachedUsageLimits(); got != nil {
		t.Fatalf("expected nil before any fetch, got %+v", got)
	}
	want := &AnthropicUsageLimits{FiveHour: usageWindow{UtilizationPct: 42}}
	p.usageCache = want
	if got := p.CachedUsageLimits(); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}
