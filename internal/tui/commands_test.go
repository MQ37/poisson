package tui

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"poisson/internal/agent"
	"poisson/internal/config"
	"poisson/internal/provider"
	"poisson/internal/store"
	"poisson/internal/testutil"
	"poisson/internal/tools"
)

// --- Test helpers for session commands ---

func newTestStoreAndAgent(t *testing.T) (*store.Store, *agent.Agent, string) {
	t.Helper()
	testutil.TempHome(t)
	dir := testutil.TempDir(t)
	dbPath := filepath.Join(dir, "test.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })


	sessionID := "test-cmd-session"
	cfg := config.DefaultConfig()
	if err := s.CreateSession(&store.Session{
		ID:        sessionID,
		Cwd:       ".",
		Provider:  "ollama",
		Model:     cfg.Ollama.Model,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	prov := provider.NewFakeProvider("ollama", []provider.Model{{ID: cfg.Ollama.Model, ContextWindow: 8192}})
	reg := tools.NewRegistry()
	a := agent.NewAgent(s, prov, reg, cfg, sessionID, make(chan agent.OutputEvent, 64), func(_, _, _ string) bool { return false })
	return s, a, sessionID
}

func newTUIWithAgent(a *agent.Agent, sessionID string) *TUI {
	t := NewTUI(a, sessionID, make(chan agent.OutputEvent, 64))
	t.rows = 24
	t.cols = 80
	t.scrollRows = 20
	t.writer = &bytes.Buffer{}
	return t
}

func cmdHost(tui *TUI) commandHost { return tuiCmdHost{tui} }

func testScrollOutput(tui *TUI) string {
	var parts []string
	for i := 0; i < tui.scroll.blockCount(); i++ {
		parts = append(parts, tui.scroll.blockRaw(i))
	}
	return strings.Join(parts, "\n")
}

// --- /new ---

func TestCmdNew(t *testing.T) {
	s, a, originalID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, originalID)

	if err := cmdNew(cmdHost(tui)); err != nil {
		t.Fatalf("cmdNew: %v", err)
	}
	out := testScrollOutput(tui)
	if !strings.Contains(out, "new session") {
		t.Errorf("expected 'new session', got %q", out)
	}
	if tui.sessionID == originalID {
		t.Error("sessionID should have changed")
	}
	if _, err := s.GetSession(tui.sessionID); err == nil {
		t.Error("new session should not be persisted until the first message")
	}
}

func TestCmdNewResetsToolCounts(t *testing.T) {
	_, a, originalID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, originalID)
	// Leftover counts from the previous session's status events.
	tui.status.ToolCalls = 7
	tui.status.ToolErrors = 3

	if err := cmdNew(cmdHost(tui)); err != nil {
		t.Fatalf("cmdNew: %v", err)
	}
	if tui.status.ToolCalls != 0 || tui.status.ToolErrors != 0 {
		t.Fatalf("tool counts not reset on /new: %dT/%de", tui.status.ToolCalls, tui.status.ToolErrors)
	}
}

// --- /name ---

func TestCmdName(t *testing.T) {
	s, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	if err := cmdName(cmdHost(tui), nil); err != nil {
		t.Fatalf("cmdName show unset: %v", err)
	}
	if out := testScrollOutput(tui); !strings.Contains(out, "title: (unset)") {
		t.Errorf("expected unset title, got %q", out)
	}

	if err := cmdName(cmdHost(tui), []string{"Poisson", "experiments"}); err != nil {
		t.Fatalf("cmdName set: %v", err)
	}
	if out := testScrollOutput(tui); !strings.Contains(out, "session title: Poisson experiments") {
		t.Errorf("expected set confirmation, got %q", out)
	}
	got, err := s.GetSession(sessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.Title == nil || *got.Title != "Poisson experiments" {
		t.Fatalf("title = %v, want %q", got.Title, "Poisson experiments")
	}

	tui.scroll = newScrollback(1024)
	if err := cmdName(cmdHost(tui), nil); err != nil {
		t.Fatalf("cmdName show set: %v", err)
	}
	if out := testScrollOutput(tui); !strings.Contains(out, "title: Poisson experiments") {
		t.Errorf("expected saved title, got %q", out)
	}
}

// --- /resume ---

func TestCmdResume(t *testing.T) {
	s, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	otherID := "other-session"
	s.CreateSession(&store.Session{
		ID: otherID, Cwd: ".", Provider: "ollama", Model: a.Config().Ollama.Model,
		CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(),
	})
	tui.scroll.appendRaw(styleAssistant, "stale-marker from previous session")

	if err := cmdResume(cmdHost(tui), []string{otherID}); err != nil {
		t.Fatalf("cmdResume: %v", err)
	}
	if tui.sessionID != otherID {
		t.Errorf("expected session %q, got %q", otherID, tui.sessionID)
	}
	if out := testScrollOutput(tui); strings.Contains(out, "stale-marker") {
		t.Errorf("resume should clear old scrollback, got %q", out)
	}
	if out := testScrollOutput(tui); !strings.Contains(out, "resumed session") {
		t.Errorf("expected resume message, got %q", out)
	}
}

func TestCmdResumeRestoresProviderAndModel(t *testing.T) {
	s, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	otherID := "xai-session"
	if err := s.CreateSession(&store.Session{
		ID: otherID, Cwd: ".", Provider: "xai", Model: a.Config().XAI.Model,
		CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("create xai session: %v", err)
	}

	cmdResume(cmdHost(tui), []string{otherID})
	if tui.sessionID != otherID {
		t.Fatalf("expected session %q, got %q", otherID, tui.sessionID)
	}
	if got := a.Provider().ID(); got != "xai" {
		t.Fatalf("provider = %q, want xai", got)
	}
	if got := a.Model(); got != a.Config().XAI.Model {
		t.Fatalf("model = %q, want %q", got, a.Config().XAI.Model)
	}
}

func TestCmdResumeNotFound(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	cmdResume(cmdHost(tui), []string{"nonexistent"})
	if out := testScrollOutput(tui); !strings.Contains(out, "session not found") {
		t.Errorf("expected not found, got %q", out)
	}
}

func TestCmdResumeNoArg(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	cmdResume(cmdHost(tui), nil)
	if out := testScrollOutput(tui); !strings.Contains(out, "usage") {
		t.Errorf("expected usage, got %q", out)
	}
}

// --- /sessions ---

func TestCmdSessions(t *testing.T) {
	s, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	s.CreateSession(&store.Session{
		ID:        "other-session",
		Cwd:       ".",
		Provider:  "fake",
		Model:     "test-model",
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	})

	cmdSessions(cmdHost(tui))
	out := testScrollOutput(tui)
	if !strings.Contains(out, "test-cmd-session") {
		t.Errorf("expected current session in output, got %q", out)
	}
	if !strings.Contains(out, "other-session") {
		t.Errorf("expected other session in output, got %q", out)
	}
}

func TestCmdSessionsEmpty(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)
	dir := testutil.TempDir(t)
	dbPath := filepath.Join(dir, "empty.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open empty store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	tui.agent = agent.NewAgent(s, provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 4096}}), tools.NewRegistry(), config.DefaultConfig(), sessionID, make(chan agent.OutputEvent, 64), func(_, _, _ string) bool { return false })
	cmdSessions(cmdHost(tui))
	if out := testScrollOutput(tui); !strings.Contains(out, "no sessions") {
		t.Errorf("expected no sessions, got %q", out)
	}
}

// --- /search ---

func TestCmdSearch(t *testing.T) {
	s, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	s.AppendMessage(&store.Message{
		SessionID: sessionID,
		Role:      "user",
		Content:   `{"text":"hello world"}`,
	})

	cmdSearch(cmdHost(tui), []string{"hello"})
	out := testScrollOutput(tui)
	if !strings.Contains(out, "[hello]") {
		t.Errorf("expected search result with highlighted term, got %q", out)
	}
}

func TestCmdSearchNoResults(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	cmdSearch(cmdHost(tui), []string{"nonexistent"})
	if out := testScrollOutput(tui); !strings.Contains(out, "no results") {
		t.Errorf("expected no results, got %q", out)
	}
}

func TestCmdSearchNoQuery(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	cmdSearch(cmdHost(tui), nil)
	if out := testScrollOutput(tui); !strings.Contains(out, "usage") {
		t.Errorf("expected usage, got %q", out)
	}
}

// --- /model ---

func TestCmdModel(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	cmdModel(cmdHost(tui), []string{"ollama/test-model-2"})
	if a.Model() != "test-model-2" {
		t.Errorf("expected model test-model-2, got %q", a.Model())
	}
	if out := testScrollOutput(tui); !strings.Contains(out, "test-model-2") {
		t.Errorf("expected model message, got %q", out)
	}
}

func TestCmdModelWithProvider(t *testing.T) {
	s, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	cmdModel(cmdHost(tui), []string{"xai/grok-build"})
	if a.Provider().ID() != "xai" {
		t.Errorf("expected provider xai, got %q", a.Provider().ID())
	}
	if a.Model() != "grok-build" {
		t.Errorf("expected model grok-build, got %q", a.Model())
	}
	sess, err := s.GetSession(sessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if sess.Provider != "xai" || sess.Model != "grok-build" {
		t.Fatalf("session metadata = %s/%s, want xai/grok-build", sess.Provider, sess.Model)
	}
}

func TestCmdModelUpdatesContextWindow(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)
	tui.status.ContextWindow = 12345 // stale value from a previous model

	cmdModel(cmdHost(tui), []string{"xai/grok-build"})

	ms, ok := provider.GetModelSettings("xai", "grok-build")
	if !ok {
		t.Fatal("missing model settings for xai/grok-build")
	}
	if tui.status.ContextWindow != ms.ContextWindow {
		t.Fatalf("ContextWindow = %d after switch, want %d", tui.status.ContextWindow, ms.ContextWindow)
	}
}

func TestCmdModelProviderOnlyResetsToDefault(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	cmdModel(cmdHost(tui), []string{"ollama/custom"})
	cmdModel(cmdHost(tui), []string{"ollama"})
	if a.Model() != a.Config().Ollama.Model {
		t.Fatalf("model = %q, want default %q", a.Model(), a.Config().Ollama.Model)
	}
	if out := testScrollOutput(tui); !strings.Contains(out, "model: ollama/") {
		t.Fatalf("expected model output, got %q", out)
	}
}

func TestCmdModelRejectsEmptyModel(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	cmdModel(cmdHost(tui), []string{"ollama/"})
	if out := testScrollOutput(tui); !strings.Contains(out, "usage") {
		t.Fatalf("expected usage, got %q", out)
	}
}

func TestCmdModelNoArg(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	cmdModel(cmdHost(tui), nil)
	if out := testScrollOutput(tui); !strings.Contains(out, "current") {
		t.Errorf("expected current model message, got %q", out)
	}
}

// --- /cost ---

func TestCmdCost(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	cmdCost(cmdHost(tui))
	out := testScrollOutput(tui)
	if !strings.Contains(out, "tokens") {
		t.Errorf("expected token output, got %q", out)
	}
}

func TestCmdCostEmpty(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	cmdCost(cmdHost(tui))
	out := testScrollOutput(tui)
	if !strings.Contains(out, "Cost") && !strings.Contains(out, "calls") {
		t.Errorf("expected cost output, got %q", out)
	}
}

func TestCmdCostEphemeralSession(t *testing.T) {
	testutil.TempHome(t)
	dir := testutil.TempDir(t)
	s, err := store.Open(filepath.Join(dir, "ephemeral.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	sessionID := store.NewSessionID()
	cfg := config.DefaultConfig()
	prov := provider.NewFakeProvider("ollama", []provider.Model{{ID: cfg.Ollama.Model, ContextWindow: 8192}})
	a := agent.NewAgent(s, prov, tools.NewRegistry(), cfg, sessionID, make(chan agent.OutputEvent, 64), func(_, _, _ string) bool { return false })
	tui := newTUIWithAgent(a, sessionID)

	cmdCost(cmdHost(tui))
	out := testScrollOutput(tui)
	if !strings.Contains(out, "not saved yet") {
		t.Errorf("expected ephemeral hint, got %q", out)
	}
}

// --- /reload ---

func TestCmdReload(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	cmdReload(cmdHost(tui))
	if out := testScrollOutput(tui); !strings.Contains(out, "reloaded") {
		t.Errorf("expected reload message, got %q", out)
	}
	if _, ok := a.Provider().(*provider.FakeProvider); ok {
		t.Fatal("provider was not rebuilt after reload")
	}
}

// --- /compact ---

func TestCmdCompactStub(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	if out := testScrollOutput(tui); out != "" {
		t.Logf("scroll output before compact: %q", out)
	}
}

// --- /effort ---

func TestCmdEffort(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	cmdEffort(cmdHost(tui), []string{"high"})
	if a.Effort() != "high" {
		t.Errorf("expected effort high, got %q", a.Effort())
	}
	if out := testScrollOutput(tui); !strings.Contains(out, "high") {
		t.Errorf("expected effort message, got %q", out)
	}
}

func TestCmdEffortInvalid(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	cmdEffort(cmdHost(tui), []string{"bogus"})
	if out := testScrollOutput(tui); !strings.Contains(out, "unknown") {
		t.Errorf("expected unknown effort message, got %q", out)
	}
}

func TestCmdEffortNoArg(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	cmdEffort(cmdHost(tui), nil)
	if out := testScrollOutput(tui); !strings.Contains(out, "current") {
		t.Errorf("expected current effort message, got %q", out)
	}
}
