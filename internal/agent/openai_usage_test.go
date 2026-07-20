package agent

import (
	"context"
	"testing"

	"github.com/mq37/poisson/internal/auth"
	"github.com/mq37/poisson/internal/config"
	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/testutil"
)

// TestOpenAIUsageLimits_NonOpenAIProviderIsNoop mirrors
// TestAnthropicUsageLimits_NonAnthropicProviderIsNoop.
func TestOpenAIUsageLimits_NonOpenAIProviderIsNoop(t *testing.T) {
	s := newTestStore(t)
	cfg := newTestConfig()
	sessionID := newTestSession(t, s, "test-model")
	reg := newTestRegistry(testutil.TempDir(t))
	a := NewAgent(s, newFakeProvider(), reg, cfg, sessionID, nil, nil)

	if got := a.OpenAIUsageLimits(); got != nil {
		t.Fatalf("expected nil for a non-OpenAI provider, got %+v", got)
	}
	a.RefreshOpenAIUsageLimits(context.Background()) // must not panic or block
	if got := a.OpenAIUsageLimits(); got != nil {
		t.Fatalf("expected still nil after refresh, got %+v", got)
	}
	if _, err := a.ResetOpenAIUsage(context.Background()); err == nil {
		t.Fatal("expected an error resetting usage on a non-OpenAI provider")
	}
}

// TestOpenAIUsageLimits_NoCredsNeverHitsNetwork exercises the real
// delegation path (Agent -> *provider.OpenAIProvider, not a fake) with no
// "openai" auth entry at all — oauthCreds's gate rejects this before any
// HTTP request is built (confirmed at the provider layer), so this never
// reaches api.openai.com/chatgpt.com even though the provider's real
// endpoints aren't overridable from outside internal/provider.
func TestOpenAIUsageLimits_NoCredsNeverHitsNetwork(t *testing.T) {
	s := newTestStore(t)
	cfg := newTestConfig()
	sessionID := newTestSession(t, s, "test-model")
	reg := newTestRegistry(testutil.TempDir(t))
	op := provider.NewOpenAIProvider(auth.AuthStore{}, &config.Config{})
	a := NewAgent(s, op, reg, cfg, sessionID, nil, nil)

	if got := a.OpenAIUsageLimits(); got != nil {
		t.Fatalf("expected nil before any fetch, got %+v", got)
	}
	a.RefreshOpenAIUsageLimits(context.Background())
	if got := a.OpenAIUsageLimits(); got != nil {
		t.Fatalf("expected still nil (no creds means no fetch ever happens), got %+v", got)
	}
	if _, err := a.ResetOpenAIUsage(context.Background()); err == nil {
		t.Fatal("expected an error resetting usage with no credentials")
	}
}
