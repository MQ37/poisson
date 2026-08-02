package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/mq37/poisson/internal/auth"
	"github.com/mq37/poisson/internal/config"
	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/testutil"
)

// TestAnthropicUsageLimits_NonAnthropicProviderIsNoop confirms both
// AnthropicUsageLimits and RefreshAnthropicUsageLimits are safe, silent
// no-ops for every provider except Anthropic (the FakeProvider used
// everywhere else in this test package never touches the network, so this
// also doubles as the "type assertion correctly fails closed" check).
func TestAnthropicUsageLimits_NonAnthropicProviderIsNoop(t *testing.T) {
	s := newTestStore(t)
	cfg := newTestConfig()
	sessionID := newTestSession(t, s, "test-model")
	reg := newTestRegistry(testutil.TempDir(t))
	a := NewAgent(s, newFakeProvider(), reg, cfg, sessionID, nil, nil)

	if got := a.AnthropicUsageLimits(); got != nil {
		t.Fatalf("expected nil for a non-Anthropic provider, got %+v", got)
	}
	a.RefreshAnthropicUsageLimits(context.Background()) // must not panic or block
	if got := a.AnthropicUsageLimits(); got != nil {
		t.Fatalf("expected still nil after refresh, got %+v", got)
	}
}

// TestAnthropicUsageLimits_APIKeyAuthNeverHitsNetwork exercises the real
// delegation path (Agent -> *provider.AnthropicProvider, not a fake) with
// api-key auth, which internal/provider/anthropic_usage.go's UsageLimits
// rejects before ever constructing an HTTP request (confirmed at the
// provider layer by TestAnthropicUsageLimits_RequiresOAuth). This is the one
// way to exercise the real provider type from this package without a live
// call ever reaching api.anthropic.com — the provider's baseURL isn't
// overridable from outside internal/provider, so this test relies on that
// same auth gate, never on a network stub, to stay offline.
func TestAnthropicUsageLimits_APIKeyAuthNeverHitsNetwork(t *testing.T) {
	s := newTestStore(t)
	cfg := newTestConfig()
	sessionID := newTestSession(t, s, "test-model")
	reg := newTestRegistry(testutil.TempDir(t))
	ap := provider.NewAnthropicProvider(auth.AuthStore{"anthropic": {Type: "api_key", Key: "sk-ant-test"}}, &config.Config{})
	a := NewAgent(s, ap, reg, cfg, sessionID, nil, nil)

	if got := a.AnthropicUsageLimits(); got != nil {
		t.Fatalf("expected nil before any fetch, got %+v", got)
	}
	a.RefreshAnthropicUsageLimits(context.Background())
	if got := a.AnthropicUsageLimits(); got != nil {
		t.Fatalf("expected still nil (api-key auth is rejected before any fetch), got %+v", got)
	}
}

// TestRefreshAnthropicUsageLimitsForce_ReachesProviderForceEachCall proves
// the agent-layer Force entry point actually reaches
// provider.AnthropicProvider.ForceUsageRefresh — the TTL-bypassing method,
// not the TTL-cached UsageLimits — on EVERY call, not just a first one: two
// calls in quick succession must produce two real server hits.
// SetBaseURLForTests is the exported test hook (internal/provider/
// test_helpers.go) that makes this reachable from outside the provider
// package without a live network call.
func TestRefreshAnthropicUsageLimitsForce_ReachesProviderForceEachCall(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{"five_hour":{"utilization":1,"resets_at":"2026-01-01T00:00:00Z"},"seven_day":{"utilization":1,"resets_at":"2026-01-01T00:00:00Z"},"extra_usage":{"is_enabled":false}}`))
	}))
	defer srv.Close()

	ap := provider.NewAnthropicProvider(auth.AuthStore{"anthropic": {Type: "oauth", Access: "t"}}, &config.Config{})
	ap.SetBaseURLForTests(srv.URL)

	s := newTestStore(t)
	cfg := newTestConfig()
	sessionID := newTestSession(t, s, "test-model")
	reg := newTestRegistry(testutil.TempDir(t))
	a := NewAgent(s, ap, reg, cfg, sessionID, nil, nil)

	a.RefreshAnthropicUsageLimitsForce(context.Background())
	a.RefreshAnthropicUsageLimitsForce(context.Background())

	if got := hits.Load(); got != 2 {
		t.Fatalf("server hits = %d, want 2 (each Force call must bypass the TTL and re-fetch)", got)
	}
}
