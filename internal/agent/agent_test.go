package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"poisson/internal/config"
	"poisson/internal/provider"
	"poisson/internal/skills"
	"poisson/internal/store"
	"poisson/internal/testutil"
	"poisson/internal/tools"
)

// --- Test helpers ----------------------------------------------------

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	dir := testutil.TempDir(t)
	dbPath := filepath.Join(dir, "test.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func newTestSession(t *testing.T, s *store.Store, model string) string {
	t.Helper()
	id := "test-session-" + strings.ReplaceAll(t.Name(), "/", "-")
	err := s.CreateSession(&store.Session{
		ID:       id,
		Cwd:      ".",
		Provider: "fake",
		Model:    model,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return id
}

func newTestConfig() *config.Config {
	return &config.Config{
		Provider:   config.ProviderConfig{Default: "fake"},
		Compaction: config.CompactionConfig{Threshold: 0.85},
	}
}

func newTestRegistry(cwd string) *tools.Registry {
	reg := tools.NewRegistry()
	reg.Register(tools.NewReadTool(cwd))
	reg.Register(tools.NewLsTool(cwd))
	reg.Register(tools.NewBashTool(cwd, true, nil)) // sandbox=true
	return reg
}

func newFakeProvider() *provider.FakeProvider {
	return provider.NewFakeProvider("fake", []provider.Model{
		{ID: "test-model", Name: "Test", ContextWindow: 8192},
	})
}

func TestDefaultModelUsesProviderModelConfig(t *testing.T) {
	cfg := &config.Config{
		Provider:  config.ProviderConfig{Default: "anthropic"},
		Anthropic: config.AnthropicConfig{Model: "claude-test"},
		XAI:       config.XAIConfig{Model: "grok-test"},
		Ollama:    config.OllamaConfig{Model: "ollama-test"},
	}

	cases := []struct {
		providerID string
		want       string
	}{
		{providerID: "anthropic", want: "claude-test"},
		{providerID: "xai", want: "grok-test"},
		{providerID: "ollama", want: "ollama-test"},
	}
	for _, tc := range cases {
		t.Run(tc.providerID, func(t *testing.T) {
			p := provider.NewFakeProvider(tc.providerID, nil)
			if got := defaultModel(p, cfg); got != tc.want {
				t.Fatalf("defaultModel(%s) = %q, want %q", tc.providerID, got, tc.want)
			}
		})
	}
}

func drainEvents(ch chan OutputEvent) *[]OutputEvent {
	events := &[]OutputEvent{}
	go func() {
		for ev := range ch {
			*events = append(*events, ev)
		}
	}()
	return events
}

// --- Tests -----------------------------------------------------------

func TestEstimateTokens(t *testing.T) {
	a := &Agent{config: newTestConfig()}
	tests := []struct {
		text     string
		expected int
	}{
		{"", 0},
		{"abc", 0},
		{"abcde", 1},
		{"abcdefgh", 2},
		{"hello world!", 3},
	}
	for _, tt := range tests {
		got := a.EstimateTokens(tt.text)
		if got != tt.expected {
			t.Errorf("EstimateTokens(%q) = %d, want %d", tt.text, got, tt.expected)
		}
	}
}

func TestEnsureSessionLazyCreate(t *testing.T) {
	s := newTestStore(t)
	id := store.NewSessionID()
	a := NewAgent(s, newFakeProvider(), newTestRegistry(testutil.TempDir(t)), newTestConfig(), id, nil, nil)

	if _, err := s.GetSession(id); err == nil {
		t.Fatal("session should not exist before first message")
	}
	if err := a.EnsureSession(); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}
	sess, err := s.GetSession(id)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.Provider != "fake" {
		t.Fatalf("session provider = %q, want fake", sess.Provider)
	}
	if err := a.EnsureSession(); err != nil {
		t.Fatalf("EnsureSession second call: %v", err)
	}
}

func TestBuildRequest(t *testing.T) {
	s := newTestStore(t)
	sessionID := newTestSession(t, s, "test-model")
	cwd := testutil.TempDir(t)
	reg := newTestRegistry(cwd)
	cfg := newTestConfig()
	agent := NewAgent(s, newFakeProvider(), reg, cfg, sessionID, nil, nil)

	userContent, _ := contentBlocksToJSON([]provider.ContentBlock{
		{Type: "text", Text: "Hello, Poisson!"},
	})
	if err := s.AppendMessage(&store.Message{
		SessionID: sessionID, Role: "user", Content: userContent,
	}); err != nil {
		t.Fatalf("append user: %v", err)
	}

	asstContent, _ := contentBlocksToJSON([]provider.ContentBlock{
		{Type: "text", Text: "Hi there!"},
	})
	if err := s.AppendMessage(&store.Message{
		SessionID: sessionID, Role: "assistant", Content: asstContent,
	}); err != nil {
		t.Fatalf("append assistant: %v", err)
	}

	req, err := agent.buildRequest()
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if req.Model != "test-model" {
		t.Errorf("Model = %q, want %q", req.Model, "test-model")
	}
	if len(req.System) < 1 {
		t.Fatalf("expected at least 1 system block, got %d", len(req.System))
	}
	if len(req.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(req.Messages))
	}
	if req.Messages[0].Role != "user" || req.Messages[0].Content[0].Text != "Hello, Poisson!" {
		t.Errorf("Messages[0] mismatch: %+v", req.Messages[0])
	}
	if req.Messages[1].Role != "assistant" || req.Messages[1].Content[0].Text != "Hi there!" {
		t.Errorf("Messages[1] mismatch: %+v", req.Messages[1])
	}
	if len(req.Tools) == 0 {
		t.Fatal("expected tool definitions, got 0")
	}
}

func TestBuildRequestIncludesSkills(t *testing.T) {
	s := newTestStore(t)
	sessionID := newTestSession(t, s, "test-model")
	reg := newTestRegistry(testutil.TempDir(t))
	cfg := newTestConfig()
	agent := NewAgent(s, newFakeProvider(), reg, cfg, sessionID, nil, nil)
	agent.SetSkills(true, []skills.Skill{{Name: "review", Description: "Review code"}})

	req, err := agent.buildRequest()
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if !strings.Contains(req.System[0].Text, "Available skills:") {
		t.Fatalf("system prompt missing skills section: %q", req.System[0].Text)
	}
	if !strings.Contains(req.System[0].Text, "review") {
		t.Fatalf("system prompt missing skill name: %q", req.System[0].Text)
	}
}

func TestBuildRequestSkillsDisabled(t *testing.T) {
	s := newTestStore(t)
	sessionID := newTestSession(t, s, "test-model")
	agent := NewAgent(s, newFakeProvider(), nil, newTestConfig(), sessionID, nil, nil)
	agent.SetSkills(false, []skills.Skill{{Name: "review", Description: "x"}})

	req, err := agent.buildRequest()
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if strings.Contains(req.System[0].Text, "Available skills:") {
		t.Fatalf("skills should be omitted when disabled")
	}
}

func TestBuildRequestWithCompactionSummary(t *testing.T) {
	s := newTestStore(t)
	sessionID := newTestSession(t, s, "test-model")
	reg := newTestRegistry(testutil.TempDir(t))
	cfg := newTestConfig()
	agent := NewAgent(s, newFakeProvider(), reg, cfg, sessionID, nil, nil)

	summary := "Previous conversation was about X."
	if err := s.ApplyCompaction(sessionID, 0, summary); err != nil {
		t.Fatalf("set compaction summary: %v", err)
	}

	userContent, _ := contentBlocksToJSON([]provider.ContentBlock{
		{Type: "text", Text: "Continue."},
	})
	if err := s.AppendMessage(&store.Message{
		SessionID: sessionID, Role: "user", Content: userContent,
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	req, err := agent.buildRequest()
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if len(req.System) != 2 {
		t.Fatalf("expected 2 system blocks, got %d", len(req.System))
	}
	if req.System[1].Text != summary {
		t.Errorf("System[1] = %q, want %q", req.System[1].Text, summary)
	}
}

func TestShouldCompact(t *testing.T) {
	s := newTestStore(t)
	sessionID := newTestSession(t, s, "test-model")
	cfg := newTestConfig()
	cfg.Compaction.Threshold = 0.01
	agent := NewAgent(s, newFakeProvider(), nil, cfg, sessionID, nil, nil)

	if err := s.RecordAPICall(&store.APICall{
		SessionID: sessionID, Seq: 1, Model: "test-model",
		InputTokens: 100, OutputTokens: 10,
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	if !agent.ShouldCompact() {
		t.Errorf("ShouldCompact() = false, want true (100 > 0.01 * 8192)")
	}

	cfg.Compaction.Threshold = 0.85
	if agent.ShouldCompact() {
		t.Errorf("ShouldCompact() with 0.85 = true, want false")
	}
}

func TestShouldCompactAtThreshold(t *testing.T) {
	s := newTestStore(t)
	sessionID := newTestSession(t, s, "test-model")
	cfg := newTestConfig()
	cfg.Compaction.Threshold = 0.85
	agent := NewAgent(s, newFakeProvider(), nil, cfg, sessionID, nil, nil)

	// 85% of 8192 = 6963.2 → ceil triggers at 6964.
	if err := s.RecordAPICall(&store.APICall{
		SessionID: sessionID, Seq: 1, Model: "test-model",
		InputTokens: 6964, OutputTokens: 10,
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if !agent.ShouldCompact() {
		t.Error("ShouldCompact() = false at 85% context, want true")
	}
}

func TestShouldCompactUsesMessageEstimateWhenLower(t *testing.T) {
	s := newTestStore(t)
	sessionID := newTestSession(t, s, "test-model")
	cfg := newTestConfig()
	cfg.Compaction.Threshold = 0.85
	agent := NewAgent(s, newFakeProvider(), nil, cfg, sessionID, nil, nil)

	if err := s.RecordAPICall(&store.APICall{
		SessionID: sessionID, Seq: 1, Model: "test-model",
		InputTokens: 7000, OutputTokens: 10,
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := s.ApplyCompaction(sessionID, 1, "summary"); err != nil {
		t.Fatalf("compact: %v", err)
	}
	used, _ := agent.ContextTokens()
	if used >= 7000 {
		t.Fatalf("context after compaction = %d, want << 7000", used)
	}
}

func TestContextTokensPrefersLargerAndDropsStale(t *testing.T) {
	s := newTestStore(t)
	sessionID := newTestSession(t, s, "test-model")
	agent := NewAgent(s, newFakeProvider(), nil, newTestConfig(), sessionID, nil, nil)

	// Real usage present, larger than the (empty) char/4 estimate -> use real.
	if err := s.RecordAPICall(&store.APICall{
		SessionID: sessionID, Seq: 1, Model: "test-model",
		InputTokens: 5000, OutputTokens: 20, CacheReadTokens: 1000,
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if used, _ := agent.ContextTokens(); used != 6020 {
		t.Fatalf("context = %d, want 6020 (input+output+cacheRead, the larger value)", used)
	}

	// After a compaction the pre-compaction usage is stale -> fall back to the
	// (smaller) message estimate instead of the inflated 6020.
	if err := s.ApplyCompaction(sessionID, 1, "summary"); err != nil {
		t.Fatalf("compact: %v", err)
	}
	if used, _ := agent.ContextTokens(); used >= 6020 {
		t.Fatalf("context after compaction = %d, want the smaller estimate", used)
	}

	// A fresh real call after the compaction is trusted again.
	if err := s.RecordAPICall(&store.APICall{
		SessionID: sessionID, Seq: 2, Model: "test-model", InputTokens: 3000,
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if used, _ := agent.ContextTokens(); used != 3000 {
		t.Fatalf("context after fresh call = %d, want 3000", used)
	}
}

func TestContextTokensAnchorPlusTrailing(t *testing.T) {
	s := newTestStore(t)
	sessionID := newTestSession(t, s, "test-model")
	agent := NewAgent(s, newFakeProvider(), nil, newTestConfig(), sessionID, nil, nil)

	call := &store.APICall{SessionID: sessionID, Seq: 1, Model: "test-model", InputTokens: 5000, OutputTokens: 100}
	if err := s.RecordAPICall(call); err != nil {
		t.Fatalf("record: %v", err)
	}
	// user (covered by anchor), assistant produced by the call (the anchor), and
	// a trailing tool result appended after it.
	if err := s.AppendMessage(&store.Message{SessionID: sessionID, Role: "user", Content: strings.Repeat("u", 8000)}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage(&store.Message{SessionID: sessionID, Role: "assistant", Content: "ok", APICallID: &call.ID}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage(&store.Message{SessionID: sessionID, Role: "tool", Content: strings.Repeat("x", 400)}); err != nil {
		t.Fatal(err)
	}

	// anchor (input+output = 5100) + trailing (only the 400-char tool msg = 100),
	// NOT the 8000-char user msg which the anchor already accounts for.
	if used, _ := agent.ContextTokens(); used != 5200 {
		t.Fatalf("context = %d, want 5200 (anchor 5100 + trailing 100)", used)
	}
}

func TestCompactionLimitReserve(t *testing.T) {
	s := newTestStore(t)
	sessionID := newTestSession(t, s, "test-model")
	cfg := newTestConfig()
	cfg.Compaction.Threshold = 0.85
	cfg.Compaction.ReserveTokens = 16384
	agent := NewAgent(s, newFakeProvider(), nil, cfg, sessionID, nil, nil)

	cases := []struct {
		window, want int
	}{
		{200000, 170000}, // threshold (0.85*200000) hits before window-reserve (183616)
		{32768, 16384},   // window-reserve (16384) hits before threshold (27852)
		{8192, 6963},     // reserve >= window -> ignored, fall back to threshold
	}
	for _, c := range cases {
		if got := agent.compactionLimit(c.window); got != c.want {
			t.Errorf("compactionLimit(%d) = %d, want %d", c.window, got, c.want)
		}
	}
}

func TestContentBlocksJSON(t *testing.T) {
	textJSON, err := contentBlocksToJSON([]provider.ContentBlock{
		{Type: "text", Text: "hello"},
	})
	if err != nil {
		t.Fatalf("text: %v", err)
	}
	if !strings.Contains(textJSON, `"text":"hello"`) {
		t.Errorf("missing text: %s", textJSON)
	}

	tuJSON, err := contentBlocksToJSON([]provider.ContentBlock{
		{Type: "tool_use", ToolCallID: "call_1", ToolName: "bash", ToolInput: json.RawMessage(`{"command":"ls"}`)},
	})
	if err != nil {
		t.Fatalf("tool_use: %v", err)
	}
	if !strings.Contains(tuJSON, `"tool_call_id":"call_1"`) {
		t.Errorf("missing tool_call_id: %s", tuJSON)
	}

	trJSON, err := contentBlocksToJSON([]provider.ContentBlock{
		{Type: "tool_result", ToolCallID: "call_1", ToolResult: "output here"},
	})
	if err != nil {
		t.Fatalf("tool_result: %v", err)
	}
	if !strings.Contains(trJSON, `"tool_result":"output here"`) {
		t.Errorf("missing tool_result: %s", trJSON)
	}

	msg := store.Message{Role: "user", Content: textJSON}
	pm, err := messageToProvider(msg)
	if err != nil {
		t.Fatalf("messageToProvider: %v", err)
	}
	if pm.Role != "user" || len(pm.Content) != 1 || pm.Content[0].Text != "hello" {
		t.Errorf("round-trip mismatch: %+v", pm)
	}
}

// --- Mocked agent loop tests (no real API calls) ---------------------

func TestAgentLoopTextResponse(t *testing.T) {
	s := newTestStore(t)
	sessionID := newTestSession(t, s, "test-model")
	cwd := testutil.TempDir(t)
	reg := newTestRegistry(cwd)
	cfg := newTestConfig()
	p := newFakeProvider()
	p.SetResponses([][]provider.StreamEvent{
		provider.FakeTextResponse("hello from mock", &provider.Usage{InputTokens: 12, OutputTokens: 8}),
	})

	ch := make(chan OutputEvent, 256)
	drainEvents(ch)
	agent := NewAgent(s, p, reg, cfg, sessionID, ch, nil)

	if err := agent.Prompt("say hello"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	close(ch)

	msgs, err := s.GetMessages(sessionID)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) < 2 {
		t.Fatalf("expected at least 2 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("msgs[0].Role = %q, want user", msgs[0].Role)
	}
	if msgs[1].Role != "assistant" {
		t.Errorf("msgs[1].Role = %q, want assistant", msgs[1].Role)
	}

	// Verify api_call recorded.
	lastCall, err := s.GetLastAPICall(sessionID)
	if err != nil {
		t.Fatalf("GetLastAPICall: %v", err)
	}
	if lastCall.InputTokens != 12 || lastCall.OutputTokens != 8 {
		t.Errorf("usage = in:%d out:%d, want in:12 out:8", lastCall.InputTokens, lastCall.OutputTokens)
	}

	// Verify assistant message has api_call_id.
	if msgs[1].APICallID == nil || *msgs[1].APICallID == "" {
		t.Error("assistant message should have api_call_id")
	}
}

func TestAgentLoopWithToolCall(t *testing.T) {
	s := newTestStore(t)
	sessionID := newTestSession(t, s, "test-model")
	cwd := testutil.TempDir(t)
	// Create a file for the read tool.
	testFile := filepath.Join(cwd, "hello.txt")
	if err := os.WriteFile(testFile, []byte("Hello from file!"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	reg := newTestRegistry(cwd)
	cfg := newTestConfig()
	p := newFakeProvider()
	first, second := provider.FakeToolCallResponse("read",
		map[string]interface{}{"path": testFile},
		"The file contains: Hello from file!")
	p.SetResponses([][]provider.StreamEvent{first, second})

	ch := make(chan OutputEvent, 256)
	drainEvents(ch)
	agent := NewAgent(s, p, reg, cfg, sessionID, ch, nil)

	if err := agent.Prompt("read hello.txt and tell me its contents"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	close(ch)

	msgs, err := s.GetMessages(sessionID)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	t.Logf("got %d messages", len(msgs))
	for i, m := range msgs {
		t.Logf("  msg[%d]: role=%s, len=%d", i, m.Role, len(m.Content))
	}

	// Expect: user, assistant(tool_use), tool(result), assistant(text).
	if len(msgs) < 4 {
		t.Fatalf("expected at least 4 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("msgs[0].Role = %q, want user", msgs[0].Role)
	}
	if msgs[1].Role != "assistant" {
		t.Errorf("msgs[1].Role = %q, want assistant", msgs[1].Role)
	}
	if msgs[2].Role != "tool" {
		t.Errorf("msgs[2].Role = %q, want tool", msgs[2].Role)
	}
	if msgs[3].Role != "assistant" {
		t.Errorf("msgs[3].Role = %q, want assistant", msgs[3].Role)
	}

	// Verify tool result content.
	var toolBlocks []contentBlockJSON
	if err := json.Unmarshal([]byte(msgs[2].Content), &toolBlocks); err != nil {
		t.Fatalf("parse tool content: %v", err)
	}
	hasResult := false
	for _, b := range toolBlocks {
		if b.Type == "tool_result" && strings.Contains(b.ToolResult, "Hello from file!") {
			hasResult = true
		}
	}
	if !hasResult {
		t.Errorf("tool result doesn't contain file content: %s", msgs[2].Content)
	}

	// Verify two api_calls (one per Stream call).
	if p.CallCount() != 2 {
		t.Errorf("CallCount = %d, want 2", p.CallCount())
	}
}

func TestExpediteInjectsNudgeIntoToolResult(t *testing.T) {
	s := newTestStore(t)
	sessionID := newTestSession(t, s, "test-model")
	cwd := testutil.TempDir(t)
	testFile := filepath.Join(cwd, "hello.txt")
	if err := os.WriteFile(testFile, []byte("Hello from file!"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	reg := newTestRegistry(cwd)
	cfg := newTestConfig()
	p := newFakeProvider()
	first, second := provider.FakeToolCallResponse("read",
		map[string]interface{}{"path": testFile},
		"done")
	p.SetResponses([][]provider.StreamEvent{first, second})

	ch := make(chan OutputEvent, 256)
	drainEvents(ch)
	agent := NewAgent(s, p, reg, cfg, sessionID, ch, nil)

	// Simulate the parent forwarding Ctrl+G before the turn runs; the flag is
	// consumed on the first tool-result batch.
	agent.Expedite()

	if err := agent.Prompt("read hello.txt"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	close(ch)

	msgs, err := s.GetMessages(sessionID)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	var toolBlocks []contentBlockJSON
	if err := json.Unmarshal([]byte(msgs[2].Content), &toolBlocks); err != nil {
		t.Fatalf("parse tool content: %v", err)
	}
	gotNudge := false
	for _, b := range toolBlocks {
		if b.Type == "tool_result" && strings.Contains(b.ToolResult, "wrap up immediately") {
			gotNudge = true
		}
	}
	if !gotNudge {
		t.Errorf("expedite nudge not appended to tool result: %s", msgs[2].Content)
	}
	// Flag is single-shot: consumed after one injection.
	if agent.expedite.Load() {
		t.Error("expedite flag should be cleared after injection")
	}
}

func TestRunTurnContinuesOnMaxTokens(t *testing.T) {
	s := newTestStore(t)
	sessionID := newTestSession(t, s, "test-model")
	reg := newTestRegistry(testutil.TempDir(t))
	cfg := newTestConfig()
	p := newFakeProvider()
	cut := []provider.StreamEvent{
		{Type: provider.EventTextDelta, Text: "partial answer"},
		{Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 10, OutputTokens: 5}, StopReason: "max_tokens"},
	}
	fin := []provider.StreamEvent{
		{Type: provider.EventTextDelta, Text: " and the rest"},
		{Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 12, OutputTokens: 4}, StopReason: "end_turn"},
	}
	p.SetResponses([][]provider.StreamEvent{cut, fin})

	ch := make(chan OutputEvent, 256)
	drainEvents(ch)
	agent := NewAgent(s, p, reg, cfg, sessionID, ch, nil)
	if err := agent.Prompt("go"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	close(ch)

	if p.CallCount() != 2 {
		t.Errorf("CallCount = %d, want 2 (cut + continuation)", p.CallCount())
	}
	msgs, _ := s.GetMessages(sessionID)
	foundContinue := false
	for _, m := range msgs {
		if m.Role == "user" && strings.Contains(m.Content, "Continue exactly where you left off") {
			foundContinue = true
		}
	}
	if !foundContinue {
		t.Errorf("expected a synthetic continue user turn, got %d msgs", len(msgs))
	}
	if last := msgs[len(msgs)-1]; last.Role != "assistant" || !strings.Contains(last.Content, "and the rest") {
		t.Errorf("expected final assistant continuation, got role=%s", last.Role)
	}
}

func TestRunTurnBoundsMaxTokensContinuations(t *testing.T) {
	s := newTestStore(t)
	sessionID := newTestSession(t, s, "test-model")
	reg := newTestRegistry(testutil.TempDir(t))
	cfg := newTestConfig()
	p := newFakeProvider()
	// Always cut off: the turn must stop after maxTurnContinuations, not loop forever.
	cut := []provider.StreamEvent{
		{Type: provider.EventTextDelta, Text: "more"},
		{Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 1, OutputTokens: 1}, StopReason: "max_tokens"},
	}
	var resps [][]provider.StreamEvent
	for i := 0; i < maxTurnContinuations+3; i++ {
		resps = append(resps, cut)
	}
	p.SetResponses(resps)

	ch := make(chan OutputEvent, 512)
	drainEvents(ch)
	agent := NewAgent(s, p, reg, cfg, sessionID, ch, nil)
	if err := agent.Prompt("go"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	close(ch)

	if want := maxTurnContinuations + 1; p.CallCount() != want {
		t.Errorf("CallCount = %d, want %d (initial + %d continuations)", p.CallCount(), want, maxTurnContinuations)
	}
}

func TestRunTurnRetriesEmptyResponse(t *testing.T) {
	old := emptyResponseBackoff
	emptyResponseBackoff = time.Millisecond
	defer func() { emptyResponseBackoff = old }()

	s := newTestStore(t)
	sessionID := newTestSession(t, s, "test-model")
	reg := newTestRegistry(testutil.TempDir(t))
	cfg := newTestConfig()
	p := newFakeProvider()
	// First stream is empty (no content); the retry returns real text.
	empty := []provider.StreamEvent{{Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 10}}}
	p.SetResponses([][]provider.StreamEvent{empty, provider.FakeTextResponse("here you go", nil)})

	ch := make(chan OutputEvent, 256)
	var events []OutputEvent
	done := make(chan struct{})
	go func() {
		for ev := range ch {
			events = append(events, ev)
		}
		close(done)
	}()
	agent := NewAgent(s, p, reg, cfg, sessionID, ch, nil)
	if err := agent.Prompt("hi"); err != nil {
		t.Fatalf("Prompt should recover via retry: %v", err)
	}
	close(ch)
	<-done // read events only after the collector goroutine has stopped

	if p.CallCount() != 2 {
		t.Errorf("CallCount = %d, want 2 (empty + one retry)", p.CallCount())
	}
	sawRetryNotice := false
	for _, ev := range events {
		if ev.Type == OutputError && strings.Contains(ev.Text, "retrying (1/") {
			sawRetryNotice = true
		}
	}
	if !sawRetryNotice {
		t.Errorf("expected a visible retry notice in the event stream, got %+v", events)
	}
	msgs, _ := s.GetMessages(sessionID)
	if len(msgs) < 2 || msgs[len(msgs)-1].Role != "assistant" {
		t.Fatalf("expected a final assistant message, got %d msgs", len(msgs))
	}
	if !strings.Contains(msgs[len(msgs)-1].Content, "here you go") {
		t.Errorf("assistant message missing retried content: %s", msgs[len(msgs)-1].Content)
	}
}

func TestRunTurnGivesUpAfterEmptyRetries(t *testing.T) {
	old := emptyResponseBackoff
	emptyResponseBackoff = time.Millisecond
	defer func() { emptyResponseBackoff = old }()

	s := newTestStore(t)
	sessionID := newTestSession(t, s, "test-model")
	reg := newTestRegistry(testutil.TempDir(t))
	cfg := newTestConfig()
	p := newFakeProvider()
	// No responses set: FakeProvider returns an empty done event every call, so
	// every attempt is empty and the retries are exhausted.

	ch := make(chan OutputEvent, 256)
	drainEvents(ch)
	agent := NewAgent(s, p, reg, cfg, sessionID, ch, nil)
	if err := agent.Prompt("hi"); err == nil {
		t.Fatal("expected an error after exhausting empty-response retries")
	}
	if want := maxEmptyResponseRetries + 1; p.CallCount() != want {
		t.Errorf("CallCount = %d, want %d (initial + %d retries)", p.CallCount(), want, maxEmptyResponseRetries)
	}
}

func TestAgentLoopError(t *testing.T) {
	s := newTestStore(t)
	sessionID := newTestSession(t, s, "test-model")
	reg := newTestRegistry(testutil.TempDir(t))
	cfg := newTestConfig()
	p := newFakeProvider()
	p.SetResponses([][]provider.StreamEvent{
		provider.FakeErrorResponse(errors.New("mock provider failure")),
	})

	ch := make(chan OutputEvent, 256)
	drainEvents(ch)
	agent := NewAgent(s, p, reg, cfg, sessionID, ch, nil)

	err := agent.Prompt("test")
	if err == nil {
		t.Fatal("expected error from Prompt with error provider")
	}
	if !strings.Contains(err.Error(), "mock provider failure") {
		t.Errorf("error = %q, want it to contain 'mock provider failure'", err.Error())
	}

	// User message should still be stored.
	msgs, _ := s.GetMessages(sessionID)
	if len(msgs) != 1 || msgs[0].Role != "user" {
		t.Errorf("expected 1 user message, got %d messages", len(msgs))
	}
}

func TestBuildRequestEmptyMessages(t *testing.T) {
	s := newTestStore(t)
	sessionID := newTestSession(t, s, "test-model")
	reg := newTestRegistry(testutil.TempDir(t))
	cfg := newTestConfig()
	agent := NewAgent(s, newFakeProvider(), reg, cfg, sessionID, nil, nil)

	req, err := agent.buildRequest()
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if len(req.Messages) != 0 {
		t.Errorf("expected 0 messages, got %d", len(req.Messages))
	}
	if len(req.System) != 1 {
		t.Errorf("expected 1 system block, got %d", len(req.System))
	}
}

func TestContextWindow(t *testing.T) {
	s := newTestStore(t)
	sessionID := newTestSession(t, s, "test-model")
	cfg := newTestConfig()
	agent := NewAgent(s, newFakeProvider(), nil, cfg, sessionID, nil, nil)

	cw := agent.ContextWindow()
	if cw != 8192 {
		t.Errorf("ContextWindow() = %d, want 8192", cw)
	}
}

// TestContextWindowBeforeSessionUsesConfigModel guards the lazy-session case:
// before the first message the session row doesn't exist, so currentModel()
// falls back to the provider's configured model. A missing provider case there
// made openai report the 8192 default instead of gpt-5.5's 400K window.
// TestContextTokensIncludesCachedInput guards against the caching regression:
// once prompt caching is on, the provider reports input_tokens EXCLUDING cached
// tokens (the rest land in cache read/write). The status counter must sum all
// three, or it collapses to the tiny uncached input (the "2 / 1,000,000" bug).
func TestContextTokensIncludesCachedInput(t *testing.T) {
	s := newTestStore(t)
	sessionID := newTestSession(t, s, "test-model")
	a := NewAgent(s, newFakeProvider(), nil, newTestConfig(), sessionID, nil, nil)

	// ~5000 estimated tokens of conversation in the store.
	if err := s.AppendMessage(&store.Message{SessionID: sessionID, Role: "user", Content: strings.Repeat("x", 20000)}); err != nil {
		t.Fatalf("append: %v", err)
	}
	// A cached turn: tiny uncached input, the rest served from cache.
	if err := s.RecordAPICall(&store.APICall{
		SessionID: sessionID, Model: "test-model",
		InputTokens: 2, CacheReadTokens: 5000, OutputTokens: 10,
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	if used, _ := a.ContextTokens(); used < 4000 {
		t.Fatalf("ContextTokens used = %d, want ~5000; regression counts only uncached input (2)", used)
	}
}

func TestContextWindowBeforeSessionUsesConfigModel(t *testing.T) {
	s := newTestStore(t)
	cfg := config.DefaultConfig() // OpenAI.Model defaults to gpt-5.5
	p := provider.NewFakeProvider("openai", nil)
	id := store.NewSessionID()    // deliberately not created yet
	a := NewAgent(s, p, newTestRegistry(testutil.TempDir(t)), cfg, id, nil, nil)

	if got := a.ContextWindow(); got != 400000 {
		t.Errorf("ContextWindow() before session = %d, want 400000 (gpt-5.5)", got)
	}
}

func TestPromptStoresUserMessage(t *testing.T) {
	s := newTestStore(t)
	sessionID := newTestSession(t, s, "test-model")
	reg := newTestRegistry(testutil.TempDir(t))
	cfg := newTestConfig()
	p := newFakeProvider()
	p.SetResponses([][]provider.StreamEvent{
		provider.FakeErrorResponse(errors.New("connection refused")),
	})
	agent := NewAgent(s, p, reg, cfg, sessionID, nil, nil)

	_ = agent.Prompt("hello")

	msgs, _ := s.GetMessages(sessionID)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 user message, got %d", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("msgs[0].Role = %q, want user", msgs[0].Role)
	}
}

var _ = context.Background

type blockingProvider struct {
	started chan struct{}
}

func (p *blockingProvider) ID() string { return "fake" }
func (p *blockingProvider) Models() ([]provider.Model, error) {
	return []provider.Model{{ID: "test-model", ContextWindow: 8192}}, nil
}
func (p *blockingProvider) Stream(ctx context.Context, req *provider.Request) (<-chan provider.StreamEvent, error) {
	ch := make(chan provider.StreamEvent)
	close(p.started)
	go func() {
		defer close(ch)
		<-ctx.Done()
	}()
	return ch, nil
}

type blockingTool struct {
	started chan struct{}
}

func (t blockingTool) Name() string        { return "wait" }
func (t blockingTool) Description() string { return "wait until cancelled" }
func (t blockingTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (t blockingTool) Execute(ctx context.Context, input json.RawMessage) (provider.ToolResult, error) {
	close(t.started)
	<-ctx.Done()
	return provider.ToolResult{}, ctx.Err()
}

func TestPromptWithContextCancelsStream(t *testing.T) {
	s := newTestStore(t)
	sessionID := newTestSession(t, s, "test-model")
	p := &blockingProvider{started: make(chan struct{})}
	a := NewAgent(s, p, newTestRegistry(testutil.TempDir(t)), newTestConfig(), sessionID, make(chan OutputEvent, 16), nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.PromptWithContext(ctx, "hello") }()

	select {
	case <-p.started:
	case <-time.After(time.Second):
		t.Fatal("provider stream did not start")
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("PromptWithContext error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("PromptWithContext did not return after cancel")
	}
	msgs, err := s.GetMessages(sessionID)
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("cancelled prompt left %d active messages, want 1 (user only)", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Fatalf("remaining message role = %q, want user", msgs[0].Role)
	}
}

func TestPromptWithContextCancelsToolTurnCleanly(t *testing.T) {
	s := newTestStore(t)
	sessionID := newTestSession(t, s, "test-model")
	reg := tools.NewRegistry()
	started := make(chan struct{})
	reg.Register(blockingTool{started: started})

	p := newFakeProvider()
	first, _ := provider.FakeToolCallResponse("wait", map[string]string{"x": "y"}, "done")
	p.SetResponses([][]provider.StreamEvent{first})
	a := NewAgent(s, p, reg, newTestConfig(), sessionID, make(chan OutputEvent, 64), nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.PromptWithContext(ctx, "use tool") }()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("tool did not start")
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("PromptWithContext error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("PromptWithContext did not return after tool cancel")
	}
	msgs, err := s.GetMessages(sessionID)
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("cancelled tool turn left %d messages, want 3 (user+assistant+tool_result)", len(msgs))
	}
	if msgs[2].Role != "tool" {
		t.Fatalf("last message role = %q, want tool", msgs[2].Role)
	}
}

func TestEffortIsPerAgent(t *testing.T) {
	s := newTestStore(t)
	s1 := newTestSession(t, s, "test-model")
	s2 := s1 + "-2"
	if err := s.CreateSession(&store.Session{ID: s2, Cwd: ".", Provider: "fake", Model: "test-model"}); err != nil {
		t.Fatalf("create second session: %v", err)
	}
	a1 := NewAgent(s, newFakeProvider(), nil, newTestConfig(), s1, nil, nil)
	a2 := NewAgent(s, newFakeProvider(), nil, newTestConfig(), s2, nil, nil)
	a1.SetEffort("high")
	if a1.Effort() != "high" {
		t.Fatalf("a1 effort = %q", a1.Effort())
	}
	if a2.Effort() != config.DefaultEffort {
		t.Fatalf("a2 effort leaked = %q, want default %q", a2.Effort(), config.DefaultEffort)
	}
}

func TestThinkingBlocksRoundTripThroughStore(t *testing.T) {
	// buildAssistantBlocks should put thinking first, and the store JSON
	// round-trip must preserve thinking text + signature + redacted flag.
	redacted := []provider.ContentBlock{{Type: "thinking", Redacted: true, ThinkingSignature: "ENC"}}
	blocks := buildAssistantBlocks("reasoning", "SIG", redacted, "the answer",
		[]provider.ToolCall{{ID: "t1", Name: "read", Input: json.RawMessage(`{"path":"a"}`)}})

	// Order: redacted thinking, thinking, text, tool_use.
	if blocks[0].Type != "thinking" || !blocks[0].Redacted || blocks[0].ThinkingSignature != "ENC" {
		t.Fatalf("blocks[0] = %+v, want redacted thinking", blocks[0])
	}
	if blocks[1].Type != "thinking" || blocks[1].Thinking != "reasoning" || blocks[1].ThinkingSignature != "SIG" {
		t.Fatalf("blocks[1] = %+v, want signed thinking", blocks[1])
	}
	if blocks[2].Type != "text" || blocks[3].Type != "tool_use" {
		t.Fatalf("unexpected block order: %+v", blocks)
	}

	js, err := contentBlocksToJSON(blocks)
	if err != nil {
		t.Fatalf("contentBlocksToJSON: %v", err)
	}
	msg, err := messageToProvider(store.Message{Role: "assistant", Content: js})
	if err != nil {
		t.Fatalf("messageToProvider: %v", err)
	}
	if len(msg.Content) != 4 {
		t.Fatalf("round-trip lost blocks: %d", len(msg.Content))
	}
	if msg.Content[1].Thinking != "reasoning" || msg.Content[1].ThinkingSignature != "SIG" {
		t.Fatalf("thinking not preserved: %+v", msg.Content[1])
	}
	if !msg.Content[0].Redacted || msg.Content[0].ThinkingSignature != "ENC" {
		t.Fatalf("redacted thinking not preserved: %+v", msg.Content[0])
	}
}

func TestEffectiveEffort(t *testing.T) {
	cases := []struct {
		effort, prov, model, want string
	}{
		// glm-5.2:cloud supports only high/max — medium is clamped to high.
		{"medium", "ollama", "glm-5.2:cloud", "high"},
		{"high", "ollama", "glm-5.2:cloud", "high"},
		{"max", "ollama", "glm-5.2:cloud", "max"},
		// claude-opus supports medium.
		{"medium", "anthropic", "claude-opus-4-8", "medium"},
		// xAI grok-build supports only high/max.
		{"medium", "xai", "grok-build", ""},  // SupportsEffort=false
		// Unknown model keeps the effort.
		{"medium", "fake", "test-model", "medium"},
		// Empty effort stays empty.
		{"", "ollama", "glm-5.2:cloud", ""},
	}
	for _, c := range cases {
		got := effectiveEffort(c.effort, c.prov, c.model)
		if got != c.want {
			t.Errorf("effectiveEffort(%q, %q, %q) = %q, want %q", c.effort, c.prov, c.model, got, c.want)
		}
	}
}
