package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/auth"
	"github.com/mq37/poisson/internal/provider"
)

// execTool runs a registered tool and returns its error string ("" on success).
func execToolError(t *testing.T, a *Agent, name string, args map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	tool, ok := a.tools.Get(name)
	if !ok {
		t.Fatalf("tool %q not registered", name)
	}
	res, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("execute %s: %v", name, err)
	}
	return res.Error
}

// TestWebToolsAnthropicBackendFollowsProvider is the gate the whole feature
// rests on: the Anthropic search/summarize backends must appear when the
// session runs on Anthropic and disappear the moment it doesn't — a stale
// backend would spend an account the user already switched away from.
func TestWebToolsAnthropicBackendFollowsProvider(t *testing.T) {
	s := newTestStore(t)
	sid := newTestSession(t, s, "test-model")
	a := NewAgent(s, newFakeProvider(), newTestRegistry("."), newTestConfig(), sid, make(chan OutputEvent, 8), nil)

	// Fake (non-Anthropic) provider: both tools must reject provider=anthropic.
	a.ReloadConfigDependentTools()
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"web_search", map[string]any{"query": "q", "provider": "anthropic"}},
		{"fetch", map[string]any{"url": "https://example.com", "provider": "anthropic"}},
	} {
		if got := execToolError(t, a, tc.tool, tc.args); !strings.Contains(got, "needs an Anthropic session") {
			t.Errorf("%s on a non-Anthropic session: error = %q, want a rejection", tc.tool, got)
		}
	}

	// Switch to Anthropic: the backend is wired, which both tools advertise in
	// the description the model reads. Asserted through the description rather
	// than by executing, so the test never talks to api.anthropic.com.
	ap := provider.NewAnthropicProvider(auth.AuthStore{
		"anthropic": auth.AuthEntry{Type: "api_key", Key: "sk-test"},
	}, newTestConfig())
	if err := a.SetProvider(ap); err != nil {
		t.Fatalf("SetProvider: %v", err)
	}
	a.ReloadConfigDependentTools()
	for _, name := range []string{"web_search", "fetch"} {
		tool, ok := a.tools.Get(name)
		if !ok {
			t.Fatalf("tool %q not registered", name)
		}
		if !strings.Contains(tool.Description(), "provider=anthropic") {
			t.Errorf("%s does not offer provider=anthropic on an Anthropic session", name)
		}
	}

	// And back off Anthropic: the backend must be dropped again.
	if err := a.SetProvider(newFakeProvider()); err != nil {
		t.Fatalf("SetProvider: %v", err)
	}
	a.ReloadConfigDependentTools()
	if got := execToolError(t, a, "fetch", map[string]any{"url": "https://example.com", "provider": "anthropic"}); !strings.Contains(got, "needs an Anthropic session") {
		t.Errorf("fetch after leaving Anthropic: error = %q, want a rejection", got)
	}
}

// TestFetchOllamaBackendGatedOffOllama covers the other half of the gating:
// provider=ollama is only available while an Ollama session is active, and the
// default backend on any other provider stays the plain fetch.
func TestFetchOllamaBackendGatedOffOllama(t *testing.T) {
	s := newTestStore(t)
	sid := newTestSession(t, s, "test-model")
	a := NewAgent(s, newFakeProvider(), newTestRegistry("."), newTestConfig(), sid, make(chan OutputEvent, 8), nil)
	a.ReloadConfigDependentTools()

	if got := execToolError(t, a, "fetch", map[string]any{"url": "https://example.com", "provider": "ollama"}); !strings.Contains(got, "needs a reachable Ollama session") {
		t.Errorf("error = %q, want an Ollama-session rejection", got)
	}
}
