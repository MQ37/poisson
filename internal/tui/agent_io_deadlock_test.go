package tui

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/agent"
	"github.com/mq37/poisson/internal/config"
	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/store"
	"github.com/mq37/poisson/internal/testutil"
	"github.com/mq37/poisson/internal/tools"
)

// TestHandleEventCompactedDoesNotBlockOnFullOutputChan is the regression
// guard for the OutputCompacted self-deadlock: handleEvent runs on
// outputChan's sole reader goroutine, under t.mu, so any Agent method it
// calls that sends on that same channel (e.g. the old t.agent.UpdateStatus())
// blocks forever once the buffer fills — no one is left to drain it. The
// agent's own outputChan is built with a 1-slot buffer and already full, so
// a call that still tried to send on it would hang; handleEvent must return
// promptly regardless, and syncHeaderFromAgentLocked must still have run.
func TestHandleEventCompactedDoesNotBlockOnFullOutputChan(t *testing.T) {
	testutil.TempHome(t)
	dir := testutil.TempDir(t)
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	sessionID := "test-deadlock-session"
	cfg := config.DefaultConfig()
	if err := s.CreateSession(&store.Session{
		ID: sessionID, Cwd: ".", Provider: "ollama", Model: cfg.Ollama.Model,
		CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	prov := provider.NewFakeProvider("ollama", []provider.Model{{ID: cfg.Ollama.Model, ContextWindow: 8192}})
	outputChan := make(chan agent.OutputEvent, 1)
	outputChan <- agent.OutputEvent{Type: agent.OutputText, Text: "filling the only slot"}
	a := agent.NewAgent(s, prov, tools.NewRegistry(), cfg, sessionID, outputChan,
		func(context.Context, string, string, string) (bool, string) { return false, "" })

	tui := newTUIWithAgent(a, sessionID)

	done := make(chan struct{})
	go func() {
		tui.mu.Lock()
		tui.handleEvent(agent.OutputEvent{
			Type:                   agent.OutputCompacted,
			CompactionTokensBefore: 9000,
			CompactionTokensAfter:  2100,
		})
		tui.mu.Unlock()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handleEvent(OutputCompacted) blocked with a full agent outputChan — deadlock reintroduced")
	}

	tui.mu.Lock()
	model := tui.status.Model
	tui.mu.Unlock()
	if model == "" {
		t.Fatal("syncHeaderFromAgentLocked did not run — header left stale")
	}
}
