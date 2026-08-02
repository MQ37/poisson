package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/mq37/poisson/internal/auth"
	"github.com/mq37/poisson/internal/config"
	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/testutil"
)

// fakeOpenAIJWT builds a minimal unsigned JWT carrying the
// chatgpt_account_id claim OpenAIProvider's usage path requires
// (extractAccountID in internal/provider/openai.go) — reproduced here since
// that package's own equivalent test helper (fakeJWT) is unexported and
// this is a different package.
func fakeOpenAIJWT(accountID string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	claims := map[string]any{"https://api.openai.com/auth": map[string]string{"chatgpt_account_id": accountID}}
	payloadJSON, _ := json.Marshal(claims)
	payload := base64.RawURLEncoding.EncodeToString(payloadJSON)
	return header + "." + payload + ".sig"
}

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

// TestRefreshOpenAIUsageLimitsForce_ReachesProviderForceEachCall is the
// OpenAI/Codex counterpart of
// TestRefreshAnthropicUsageLimitsForce_ReachesProviderForceEachCall — two
// Force calls in quick succession must produce two real server hits,
// proving the agent-layer entry point reaches
// provider.OpenAIProvider.ForceUsageRefresh on every call rather than the
// TTL-cached UsageLimits.
func TestRefreshOpenAIUsageLimitsForce_ReachesProviderForceEachCall(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{"rate_limit":{"primary_window":{"used_percent":1,"reset_after_seconds":1,"reset_at":1}},"rate_limit_reset_credits":{"available_count":0}}`))
	}))
	defer srv.Close()

	op := provider.NewOpenAIProvider(auth.AuthStore{"openai": {Type: "oauth", Access: fakeOpenAIJWT("acc_1"), Expires: 1 << 62}}, &config.Config{})
	op.SetWebBaseURLForTests(srv.URL)

	s := newTestStore(t)
	cfg := newTestConfig()
	sessionID := newTestSession(t, s, "test-model")
	reg := newTestRegistry(testutil.TempDir(t))
	a := NewAgent(s, op, reg, cfg, sessionID, nil, nil)

	a.RefreshOpenAIUsageLimitsForce(context.Background())
	a.RefreshOpenAIUsageLimitsForce(context.Background())

	if got := hits.Load(); got != 2 {
		t.Fatalf("server hits = %d, want 2 (each Force call must bypass the TTL and re-fetch)", got)
	}
}
