package agent

import (
	"testing"

	"github.com/mq37/poisson/internal/provider"
)

// The status-bar context counter must include the system prompt and tool
// definitions, not just message text. Before this fix the counter reported a
// near-empty context on the first message even though the system prompt (base
// instructions + AGENTS.md + tool schemas) is a couple thousand tokens: the
// "trust the lower estimate" branch always won because the messages-only
// estimate was smaller than the real input size.
func TestContextTokensIncludeSystemPrompt(t *testing.T) {
	s := newTestStore(t)
	sid := newTestSession(t, s, "test-model")
	fp := newFakeProvider()
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, make(chan OutputEvent, 8), nil)

	if got := a.sysTokensEstimate.Load(); got != 0 {
		t.Fatalf("sysTokensEstimate before first build = %d, want 0", got)
	}

	// Estimate path: no API call recorded yet. The counter must include the
	// system prompt, not just message text.
	if _, err := a.buildRequest(); err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	sysEst := int(a.sysTokensEstimate.Load())
	if sysEst < 500 {
		t.Fatalf("sysTokensEstimate = %d, want a substantial system prompt (base + tool schemas)", sysEst)
	}
	used, _ := a.ContextTokens()
	if want := sysEst + a.estimateMessagesTokens(); used != want {
		t.Fatalf("estimate-path ContextTokens = %d, want %d (system + messages)", used, want)
	}

	// Actual path: a real provider reports the full input size (~5000 here). The
	// counter must not collapse back to a tiny messages-only figure.
	fp.SetResponses([][]provider.StreamEvent{
		provider.FakeTextResponse("ok", &provider.Usage{InputTokens: 5000, OutputTokens: 3}),
	})
	if err := a.Prompt("hi"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	used, _ = a.ContextTokens()
	if msgsOnly := a.estimateMessagesTokens(); used <= msgsOnly {
		t.Fatalf("actual-path ContextTokens (%d) collapsed to the messages-only estimate (%d); system prompt ignored", used, msgsOnly)
	}
	if used < sysEst {
		t.Fatalf("actual-path ContextTokens (%d) < system prompt estimate (%d)", used, sysEst)
	}
}
