package agent

import (
	"context"
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
