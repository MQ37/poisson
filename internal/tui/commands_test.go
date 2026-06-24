package tui

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"poisson/internal/agent"
	"poisson/internal/config"
	"poisson/internal/provider"
	"poisson/internal/store"
	"poisson/internal/tools"
)

// --- Test helpers for session commands ---

func newTestStoreAndAgent(t *testing.T) (*store.Store, *agent.Agent, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.SeedPricing(); err != nil {
		t.Fatalf("seed pricing: %v", err)
	}

	sessionID := "test-cmd-session"
	if err := s.CreateSession(&store.Session{
		ID:        sessionID,
		Cwd:       ".",
		Provider:  "fake",
		Model:     "test-model",
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	p := provider.NewFakeProvider("fake", []provider.Model{
		{ID: "test-model", ContextWindow: 8192},
	})
	reg := tools.NewRegistry()
	reg.Register(tools.NewReadTool("."))
	cfg := &config.Config{
		Provider:   config.ProviderConfig{Default: "fake"},
		Compaction: config.CompactionConfig{Threshold: 0.8},
	}
	ch := make(chan agent.OutputEvent, 64)
	a := agent.NewAgent(s, p, reg, cfg, sessionID, ch, nil)

	return s, a, sessionID
}

func newTUIWithAgent(a *agent.Agent, sessionID string) *TUI {
	buf := &bytes.Buffer{}
	return &TUI{
		outputChan: make(chan agent.OutputEvent, 64),
		history:    []string{},
		histIdx:    -1,
		agent:      a,
		sessionID:  sessionID,
		writer:     buf,
	}
}

// --- /new ---

func TestCmdNew(t *testing.T) {
	s, a, originalID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, originalID)

	if err := tui.cmdNew(); err != nil {
		t.Fatalf("cmdNew: %v", err)
	}
	out := tui.output()
	if !strings.Contains(out, "new session") {
		t.Errorf("expected 'new session', got %q", out)
	}
	if tui.sessionID == originalID {
		t.Error("sessionID should have changed")
	}
	sess, err := s.GetSession(tui.sessionID)
	if err != nil {
		t.Fatalf("get new session: %v", err)
	}
	if sess.ID != tui.sessionID {
		t.Errorf("session ID mismatch")
	}
}

// --- /resume ---

func TestCmdResume(t *testing.T) {
	s, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	// Create another session to resume.
	otherID := "other-session"
	s.CreateSession(&store.Session{
		ID: otherID, Cwd: ".", Provider: "fake", Model: "test-model",
		CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(),
	})

	err := tui.cmdResume([]string{otherID})
	if err != nil {
		t.Fatalf("cmdResume: %v", err)
	}
	if tui.sessionID != otherID {
		t.Errorf("sessionID = %q, want %q", tui.sessionID, otherID)
	}
	if !strings.Contains(tui.output(), "resumed session") {
		t.Errorf("expected 'resumed session', got %q", tui.output())
	}
}

func TestCmdResumeNotFound(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	tui.cmdResume([]string{"nonexistent"})
	if !strings.Contains(tui.output(), "not found") {
		t.Errorf("expected 'not found', got %q", tui.output())
	}
}

func TestCmdResumeNoArg(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	tui.cmdResume(nil)
	if !strings.Contains(tui.output(), "usage") {
		t.Errorf("expected usage message, got %q", tui.output())
	}
}

// --- /sessions ---

func TestCmdSessions(t *testing.T) {
	s, a, sessionID := newTestStoreAndAgent(t)
	// Add a second session.
	s.CreateSession(&store.Session{
		ID: "session-two", Cwd: ".", Provider: "fake", Model: "test-model",
		CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(),
	})
	// Add some messages to the first session.
	s.AppendMessage(&store.Message{
		SessionID: sessionID, Role: "user", Content: `[{"type":"text","text":"hi"}]`,
	})

	tui := newTUIWithAgent(a, sessionID)
	tui.cmdSessions()
	out := tui.output()
	if !strings.Contains(out, sessionID[:6]) {
		t.Errorf("expected current session in list, got %q", out)
	}
	if !strings.Contains(out, "session-two"[:6]) {
		t.Errorf("expected second session in list, got %q", out)
	}
}

func TestCmdSessionsEmpty(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	s, _ := store.Open(dbPath)
	t.Cleanup(func() { s.Close() })
	s.SeedPricing()

	p := provider.NewFakeProvider("fake", nil)
	cfg := &config.Config{}
	ch := make(chan agent.OutputEvent, 64)
	a := agent.NewAgent(s, p, nil, cfg, "empty", ch, nil)

	tui := newTUIWithAgent(a, "empty")
	tui.cmdSessions()
	if !strings.Contains(tui.output(), "no sessions") {
		t.Errorf("expected 'no sessions', got %q", tui.output())
	}
}

// --- /search ---

func TestCmdSearch(t *testing.T) {
	s, a, sessionID := newTestStoreAndAgent(t)
	s.AppendMessage(&store.Message{
		SessionID: sessionID, Role: "user",
		Content: `[{"type":"text","text":"hello world from Poisson"}]`,
	})

	tui := newTUIWithAgent(a, sessionID)
	tui.cmdSearch([]string{"hello"})
	out := tui.output()
	if !strings.Contains(out, "hello") {
		t.Errorf("expected 'hello' in search results, got %q", out)
	}
}

func TestCmdSearchNoResults(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)
	tui.cmdSearch([]string{"nonexistent"})
	if !strings.Contains(tui.output(), "no results") {
		t.Errorf("expected 'no results', got %q", tui.output())
	}
}

func TestCmdSearchNoQuery(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)
	tui.cmdSearch(nil)
	if !strings.Contains(tui.output(), "usage") {
		t.Errorf("expected usage, got %q", tui.output())
	}
}

// --- /undo ---

func TestCmdUndo(t *testing.T) {
	s, a, sessionID := newTestStoreAndAgent(t)
	// Add user + assistant messages.
	s.AppendMessage(&store.Message{SessionID: sessionID, Role: "user", Content: `[{"type":"text","text":"hello"}]`})
	s.AppendMessage(&store.Message{SessionID: sessionID, Role: "assistant", Content: `[{"type":"text","text":"hi back"}]`})

	tui := newTUIWithAgent(a, sessionID)
	tui.cmdUndo()
	out := tui.output()
	if !strings.Contains(out, "soft-deleted") {
		t.Errorf("expected 'soft-deleted', got %q", out)
	}

	// Verify messages are soft-deleted (not returned by GetMessages).
	msgs, _ := s.GetMessages(sessionID)
	if len(msgs) != 0 {
		t.Errorf("expected 0 active messages after undo, got %d", len(msgs))
	}
}

func TestCmdUndoNoUserMessage(t *testing.T) {
	s, a, sessionID := newTestStoreAndAgent(t)
	// Only an assistant message, no user.
	s.AppendMessage(&store.Message{SessionID: sessionID, Role: "assistant", Content: `[{"type":"text","text":"hi"}]`})

	tui := newTUIWithAgent(a, sessionID)
	tui.cmdUndo()
	if !strings.Contains(tui.output(), "no user message") {
		t.Errorf("expected 'no user message', got %q", tui.output())
	}
}

// --- /fork ---

func TestCmdForkLatest(t *testing.T) {
	s, a, sessionID := newTestStoreAndAgent(t)
	s.AppendMessage(&store.Message{SessionID: sessionID, Role: "user", Content: `[{"type":"text","text":"hello"}]`})
	s.AppendMessage(&store.Message{SessionID: sessionID, Role: "assistant", Content: `[{"type":"text","text":"hi back"}]`})

	originalMsgs, _ := s.GetMessages(sessionID)
	tui := newTUIWithAgent(a, sessionID)
	tui.cmdFork(nil)
	out := tui.output()
	if !strings.Contains(out, "forked") {
		t.Errorf("expected 'forked', got %q", out)
	}
	if tui.sessionID == sessionID {
		t.Error("should have switched to new session")
	}

	// Verify cloned messages exist in new session.
	newMsgs, _ := s.GetMessages(tui.sessionID)
	if len(newMsgs) != len(originalMsgs) {
		t.Errorf("forked session has %d messages, want %d", len(newMsgs), len(originalMsgs))
	}
}

func TestCmdForkEmptySession(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)
	tui.cmdFork(nil)
	if !strings.Contains(tui.output(), "nothing to fork") {
		t.Errorf("expected 'nothing to fork', got %q", tui.output())
	}
}

// --- /cost ---

func TestCmdCost(t *testing.T) {
	s, a, sessionID := newTestStoreAndAgent(t)
	s.RecordAPICall(&store.APICall{
		SessionID: sessionID, Seq: 1, Model: "test-model",
		InputTokens: 100, OutputTokens: 50, Cost: 0.0123,
	})

	tui := newTUIWithAgent(a, sessionID)
	tui.cmdCost()
	out := tui.output()
	if !strings.Contains(out, "100") {
		t.Errorf("expected input tokens 100, got %q", out)
	}
	if !strings.Contains(out, "$0.0123") {
		t.Errorf("expected cost $0.0123, got %q", out)
	}
}

func TestCmdCostEmpty(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)
	tui.cmdCost()
	out := tui.output()
	if !strings.Contains(out, "Cost") {
		t.Errorf("expected cost output, got %q", out)
	}
}

// --- /model ---

func TestCmdModel(t *testing.T) {
	s, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)
	tui.cmdModel([]string{"test-model-2"})

	sess, _ := s.GetSession(sessionID)
	if sess.Model != "test-model-2" {
		t.Errorf("model = %q, want %q", sess.Model, "test-model-2")
	}
	if !strings.Contains(tui.output(), "test-model-2") {
		t.Errorf("expected model name in output, got %q", tui.output())
	}
}

func TestCmdModelWithProvider(t *testing.T) {
	s, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)
	tui.cmdModel([]string{"ollama/glm-5.2:cloud"})

	sess, _ := s.GetSession(sessionID)
	if sess.Provider != "ollama" {
		t.Errorf("provider = %q, want ollama", sess.Provider)
	}
	if sess.Model != "glm-5.2:cloud" {
		t.Errorf("model = %q, want glm-5.2:cloud", sess.Model)
	}
	if !strings.Contains(tui.output(), "ollama/glm-5.2:cloud") {
		t.Errorf("expected 'ollama/glm-5.2:cloud' in output, got %q", tui.output())
	}
}

func TestCmdModelNoArg(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)
	tui.cmdModel(nil)
	if !strings.Contains(tui.output(), "usage") {
		t.Errorf("expected usage, got %q", tui.output())
	}
}

// --- /reload ---

func TestCmdReload(t *testing.T) {
	// Override HOME so config.Load doesn't touch real ~/.poisson.
	tmpHome := t.TempDir()
	origHome, hadHome := os.LookupEnv("HOME")
	os.Setenv("HOME", tmpHome)
	t.Cleanup(func() {
		if hadHome {
			os.Setenv("HOME", origHome)
		} else {
			os.Unsetenv("HOME")
		}
	})

	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)
	tui.cmdReload()
	if !strings.Contains(tui.output(), "reloaded") {
		t.Errorf("expected 'reloaded', got %q", tui.output())
	}
}

// --- /compact stub ---

func TestCmdCompactStub(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)
	tui.handleSlashCommand("/compact")
	if !strings.Contains(tui.output(), "not yet available") {
		t.Errorf("expected 'not yet available', got %q", tui.output())
	}
}
func TestCmdEffort(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)
	tui.cmdEffort([]string{"high"})
	if a.Effort() != "high" {
		t.Errorf("effort = %q, want high", a.Effort())
	}
	if !strings.Contains(tui.output(), "high") {
		t.Errorf("expected 'high' in output, got %q", tui.output())
	}
}

func TestCmdEffortInvalid(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)
	tui.cmdEffort([]string{"bogus"})
	if !strings.Contains(tui.output(), "invalid effort") {
		t.Errorf("expected 'invalid effort', got %q", tui.output())
	}
}

func TestCmdEffortNoArg(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)
	tui.cmdEffort(nil)
	if !strings.Contains(tui.output(), "usage") {
		t.Errorf("expected usage, got %q", tui.output())
	}
}
