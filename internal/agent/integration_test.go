package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"poisson/internal/config"
	"poisson/internal/provider"
	"poisson/internal/store"
	"poisson/internal/testutil"
	"poisson/internal/tools"
)

// barrierTool is a test tool whose Execute is fully controllable, used to prove
// the agent runs a turn's tool calls concurrently.
type barrierTool struct {
	name string
	run  func(ctx context.Context) (tools.ToolResult, error)
}

func (b barrierTool) Name() string                { return b.name }
func (b barrierTool) Description() string          { return b.name }
func (b barrierTool) Schema() json.RawMessage      { return json.RawMessage(`{"type":"object"}`) }
func (b barrierTool) Execute(ctx context.Context, _ json.RawMessage) (tools.ToolResult, error) {
	return b.run(ctx)
}

// twoToolTurn builds a turn with two tool_use blocks (call_1→first, call_2→
// second) followed by a final-text turn.
func twoToolTurn(first, second, finalText string) [][]provider.StreamEvent {
	arg := json.RawMessage(`{}`)
	return [][]provider.StreamEvent{
		{
			{Type: provider.EventToolUseStart, ToolCall: &provider.ToolCall{ID: "call_1", Name: first, Input: arg}},
			{Type: provider.EventToolUseStop, ToolCall: &provider.ToolCall{ID: "call_1", Name: first, Input: arg}},
			{Type: provider.EventToolUseStart, ToolCall: &provider.ToolCall{ID: "call_2", Name: second, Input: arg}},
			{Type: provider.EventToolUseStop, ToolCall: &provider.ToolCall{ID: "call_2", Name: second, Input: arg}},
			{Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 10, OutputTokens: 5}},
		},
		provider.FakeTextResponse(finalText, nil),
	}
}

// TestInteg_ParallelToolsRunConcurrently proves a turn's tool calls execute
// concurrently (not serially) and that tool_result messages are persisted in
// tool_use order even when the later tool finishes first.
//
// Each tool signals it has started and then waits for the other's start
// signal, so both must be in flight at once to proceed — serial execution
// would deadlock (caught by the ctx timeout + the maxInFlight assertion).
// "first" sleeps briefly after the rendezvous so it finishes last; the
// ordering assertion then proves results are re-ordered by tool_use index,
// not completion time.
func TestInteg_ParallelToolsRunConcurrently(t *testing.T) {
	st := newTestStore(t)
	sid := "itest-parallel"
	if err := st.CreateSession(&store.Session{ID: sid, Cwd: testutil.TempDir(t), Provider: "fake", Model: "m", CreatedAt: time.Now().Unix()}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	prov := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	prov.SetResponses(twoToolTurn("first", "second", "done"))

	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	var inFlight, maxInFlight int32
	track := func() func() {
		n := atomic.AddInt32(&inFlight, 1)
		for {
			m := atomic.LoadInt32(&maxInFlight)
			if n <= m || atomic.CompareAndSwapInt32(&maxInFlight, m, n) {
				break
			}
		}
		return func() { atomic.AddInt32(&inFlight, -1) }
	}

	reg := tools.NewRegistry()
	reg.Register(barrierTool{name: "first", run: func(ctx context.Context) (tools.ToolResult, error) {
		defer track()()
		close(firstStarted)
		select {
		case <-secondStarted: // serial execution would deadlock here
		case <-ctx.Done():
			return tools.ToolResult{}, ctx.Err()
		}
		time.Sleep(20 * time.Millisecond) // finish after second → exercises index re-ordering
		return tools.ToolResult{Content: "FIRST_DONE"}, nil
	}})
	reg.Register(barrierTool{name: "second", run: func(ctx context.Context) (tools.ToolResult, error) {
		defer track()()
		close(secondStarted)
		select {
		case <-firstStarted:
		case <-ctx.Done():
			return tools.ToolResult{}, ctx.Err()
		}
		return tools.ToolResult{Content: "SECOND_DONE"}, nil
	}})

	cfg := config.DefaultConfig()
	cfg.Provider.Default = "fake"
	a := NewAgent(st, prov, reg, cfg, sid, make(chan OutputEvent, 256), nil)
	a.SetModel("m")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.PromptWithContext(ctx, "run both"); err != nil {
		t.Fatalf("PromptWithContext: %v", err)
	}

	if got := atomic.LoadInt32(&maxInFlight); got < 2 {
		t.Errorf("max concurrent tool executions = %d, want 2 (ran serially?)", got)
	}

	msgs, _ := st.GetMessages(sid)
	// user, assistant(tool_use x2), tool_result(first), tool_result(second), assistant(text)
	if len(msgs) != 5 {
		t.Fatalf("expected 5 messages, got %d", len(msgs))
	}
	if msgs[2].Role != "tool" || !strings.Contains(msgs[2].Content, "FIRST_DONE") {
		t.Errorf("msg[2] should be first's result in tool_use order, got %q", msgs[2].Content)
	}
	if msgs[3].Role != "tool" || !strings.Contains(msgs[3].Content, "SECOND_DONE") {
		t.Errorf("msg[3] should be second's result, got %q", msgs[3].Content)
	}
}

// =============================================================================
// Integration test framework
//
// All tests use testutil.TempDir (under /tmp) for the SQLite DB and file I/O.
// All tests use FakeProvider — zero real API calls. Each test sets up a full
// Agent with a real store, real tool registry, and mocked provider responses,
// then verifies the store state and event sequence.
// =============================================================================

// integEnv bundles the common test setup: store, agent, provider, output chan.
type integEnv struct {
	t       *testing.T
	dir     string
	store   *store.Store
	prov    *provider.FakeProvider
	agent   *Agent
	sid     string
	output  chan OutputEvent
	reg     *tools.Registry
}

// newIntegEnv creates a full agent environment under /tmp with a FakeProvider.
func newIntegEnv(t *testing.T, responses [][]provider.StreamEvent) *integEnv {
	t.Helper()
	dir := testutil.TempDir(t)
	st, err := store.Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	sid := "itest-" + t.Name()
	if err := st.CreateSession(&store.Session{
		ID:        sid,
		Cwd:       dir,
		Provider:  "fake",
		Model:     "test-model",
		CreatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	prov := provider.NewFakeProvider("fake", []provider.Model{
		{ID: "test-model", ContextWindow: 8192},
	})
	prov.SetResponses(responses)

	reg := tools.NewRegistry()
	reg.Register(tools.NewReadTool(dir, true, nil))
	reg.Register(tools.NewWriteTool(dir, true, nil))
	reg.Register(tools.NewBashTool(dir, true, nil)) // sandbox=true (auto-approve)

	cfg := config.DefaultConfig()
	cfg.Provider.Default = "fake"

	output := make(chan OutputEvent, 256)
	a := NewAgent(st, prov, reg, cfg, sid, output, nil)
	a.SetModel("test-model")

	return &integEnv{
		t:      t,
		dir:    dir,
		store:  st,
		prov:   prov,
		agent:  a,
		sid:    sid,
		output: output,
		reg:    reg,
	}
}

// drainEvents collects all OutputEvents until OutputDone (or timeout).
func (e *integEnv) drainEvents() []OutputEvent {
	var out []OutputEvent
	for {
		select {
		case ev, ok := <-e.output:
			if !ok {
				return out
			}
			out = append(out, ev)
			if ev.Type == OutputDone {
				return out
			}
		case <-time.After(5 * time.Second):
			e.t.Fatal("timed out waiting for OutputDone")
		}
	}
}

// send sends a message and returns when the turn completes.
func (e *integEnv) send(text string) []OutputEvent {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := e.agent.PromptWithContext(ctx, text)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		e.t.Fatalf("PromptWithContext: %v", err)
	}
	return e.drainEvents()
}

// msgs returns active messages from the store.
func (e *integEnv) msgs() []store.Message {
	m, err := e.store.GetMessages(e.sid)
	if err != nil {
		e.t.Fatalf("GetMessages: %v", err)
	}
	return m
}

// msgText extracts the text content from a stored message.
func msgText(m store.Message) string {
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(m.Content), &blocks); err != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "")
}

// hasBlock checks if a message contains a content block of the given type.
func hasBlock(m store.Message, blockType string) bool {
	var blocks []struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal([]byte(m.Content), &blocks)
	for _, b := range blocks {
		if b.Type == blockType {
			return true
		}
	}
	return false
}

// =============================================================================
// Tests
// =============================================================================

// TestInteg_SimpleTextConversation verifies a basic user→assistant exchange:
// the store gets a user message and an assistant message, and the text matches.
func TestInteg_SimpleTextConversation(t *testing.T) {
	e := newIntegEnv(t, [][]provider.StreamEvent{
		provider.FakeTextResponse("Hello! I can help with that.", nil),
	})

	events := e.send("hi there")

	if len(events) == 0 {
		t.Fatal("no events received")
	}

	msgs := e.msgs()
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "user" || msgText(msgs[0]) != "hi there" {
		t.Errorf("user msg = %q (%q)", msgs[0].Role, msgText(msgs[0]))
	}
	if msgs[1].Role != "assistant" || msgText(msgs[1]) != "Hello! I can help with that." {
		t.Errorf("assistant msg = %q (%q)", msgs[1].Role, msgText(msgs[1]))
	}
}

// TestInteg_ThinkingBlocksPersisted verifies that reasoning/thinking content
// is captured in the assistant message (for display + round-trip safety).
func TestInteg_ThinkingBlocksPersisted(t *testing.T) {
	e := newIntegEnv(t, [][]provider.StreamEvent{
		provider.FakeThinkingTextResponse(
			"The user said hello. I should respond warmly.",
			"Hello! Great to meet you.",
			nil,
		),
	})

	events := e.send("hello")

	// Verify thinking events were emitted.
	var sawThinking bool
	for _, ev := range events {
		if ev.Type == OutputThinking {
			sawThinking = true
		}
	}
	if !sawThinking {
		t.Error("no OutputThinking events received")
	}

	// Verify the assistant message has a thinking block.
	msgs := e.msgs()
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if !hasBlock(msgs[1], "thinking") {
		t.Error("assistant message missing thinking block")
	}
	if msgText(msgs[1]) != "Hello! Great to meet you." {
		t.Errorf("assistant text = %q", msgText(msgs[1]))
	}
}

// TestInteg_ToolCallFlow verifies the full tool-call cycle: user message →
// model requests tool → tool executes → model produces final answer.
func TestInteg_ToolCallFlow(t *testing.T) {
	dir := testutil.TempDir(t)
	st, err := store.Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	sid := "itest-tool"
	st.CreateSession(&store.Session{ID: sid, Cwd: dir, Provider: "fake", Model: "m", CreatedAt: time.Now().Unix()})

	prov := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	first, second := provider.FakeToolCallResponse("write", map[string]string{
		"path":    "output.txt",
		"content": "hello world",
	}, "I wrote the file.")
	prov.SetResponses([][]provider.StreamEvent{first, second})

	reg := tools.NewRegistry()
	reg.Register(tools.NewWriteTool(dir, true, nil))
	reg.Register(tools.NewReadTool(dir, true, nil))

	cfg := config.DefaultConfig()
	cfg.Provider.Default = "fake"
	output := make(chan OutputEvent, 256)
	a := NewAgent(st, prov, reg, cfg, sid, output, nil)
	a.SetModel("m")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a.PromptWithContext(ctx, "write a file")

	// Drain events (two stream calls = two turns).
	var doneCount int
	for {
		select {
		case ev, ok := <-output:
			if !ok {
				goto check
			}
			if ev.Type == OutputDone {
				doneCount++
				if doneCount >= 1 {
					goto check
				}
			}
		case <-time.After(10 * time.Second):
			t.Fatal("timeout waiting for both turns")
		}
	}

check:
	msgs, _ := st.GetMessages(sid)
	// Expected: user, assistant(tool_use), tool_result, assistant(text)
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("msg[0] role = %q, want user", msgs[0].Role)
	}
	if msgs[1].Role != "assistant" || !hasBlock(msgs[1], "tool_use") {
		t.Errorf("msg[1] = %q, want assistant with tool_use", msgs[1].Role)
	}
	if msgs[2].Role != "tool" {
		t.Errorf("msg[2] role = %q, want tool", msgs[2].Role)
	}
	if msgs[3].Role != "assistant" || msgText(msgs[3]) != "I wrote the file." {
		t.Errorf("msg[3] text = %q, want final answer", msgText(msgs[3]))
	}

	// Verify the file was actually written.
	r := tools.NewReadTool(dir, true, nil)
	res, _ := r.Execute(context.Background(), mustJSON(t, map[string]string{"path": "output.txt"}))
	if !strings.Contains(res.Content, "hello world") {
		t.Errorf("file content = %q, want 'hello world'", res.Content)
	}
}

// TestInteg_MultiTurnConversation verifies multiple back-to-back messages
// accumulate correctly in the store.
func TestInteg_MultiTurnConversation(t *testing.T) {
	e := newIntegEnv(t, [][]provider.StreamEvent{
		provider.FakeTextResponse("First answer.", nil),
		provider.FakeTextResponse("Second answer.", nil),
		provider.FakeTextResponse("Third answer.", nil),
	})

	e.send("question 1")
	e.send("question 2")
	e.send("question 3")

	msgs := e.msgs()
	// 3 user + 3 assistant = 6
	if len(msgs) != 6 {
		t.Fatalf("expected 6 messages, got %d", len(msgs))
	}
	for i, m := range msgs {
		if i%2 == 0 && m.Role != "user" {
			t.Errorf("msg[%d] role = %q, want user", i, m.Role)
		}
		if i%2 == 1 && m.Role != "assistant" {
			t.Errorf("msg[%d] role = %q, want assistant", i, m.Role)
		}
	}
	answers := []string{"First answer.", "Second answer.", "Third answer."}
	for i, want := range answers {
		got := msgText(msgs[1+i*2])
		if got != want {
			t.Errorf("answer %d = %q, want %q", i+1, got, want)
		}
	}
}

// TestInteg_ThinkingThenToolCall verifies a model that thinks before calling
// a tool — both the thinking block and the tool_use are in the assistant message.
func TestInteg_ThinkingThenToolCall(t *testing.T) {
	dir := testutil.TempDir(t)
	st, err := store.Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	sid := "itest-think-tool"
	st.CreateSession(&store.Session{ID: sid, Cwd: dir, Provider: "fake", Model: "m", CreatedAt: time.Now().Unix()})

	prov := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	first, second := provider.FakeThinkingToolCallResponse(
		"I need to write a file for the user.",
		"Let me create that file.",
		"write",
		map[string]string{"path": "test.txt", "content": "data"},
		"Done! The file is written.",
	)
	prov.SetResponses([][]provider.StreamEvent{first, second})

	reg := tools.NewRegistry()
	reg.Register(tools.NewWriteTool(dir, true, nil))
	reg.Register(tools.NewReadTool(dir, true, nil))

	cfg := config.DefaultConfig()
	cfg.Provider.Default = "fake"
	output := make(chan OutputEvent, 256)
	a := NewAgent(st, prov, reg, cfg, sid, output, nil)
	a.SetModel("m")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a.PromptWithContext(ctx, "create a file")

	// Drain two turns.
	var doneCount int
	for {
		select {
		case ev, ok := <-output:
			if !ok {
				goto check
			}
			if ev.Type == OutputDone {
				doneCount++
				if doneCount >= 1 {
					goto check
				}
			}
		case <-time.After(10 * time.Second):
			t.Fatal("timeout")
		}
	}

check:
	msgs, _ := st.GetMessages(sid)
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages (user+assistant+tool+assistant), got %d", len(msgs))
	}
	// First assistant message should have BOTH thinking and tool_use.
	if !hasBlock(msgs[1], "thinking") {
		t.Error("first assistant missing thinking block")
	}
	if !hasBlock(msgs[1], "tool_use") {
		t.Error("first assistant missing tool_use block")
	}
}

// TestInteg_CancelKeepsConversation verifies that cancelling a turn keeps
// the conversation visible (user message stays, no soft-delete).
func TestInteg_CancelKeepsConversation(t *testing.T) {
	dir := testutil.TempDir(t)
	st, err := store.Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	sid := "itest-cancel"
	st.CreateSession(&store.Session{ID: sid, Cwd: dir, Provider: "fake", Model: "m", CreatedAt: time.Now().Unix()})

	// Provider that blocks during streaming (simulates a long-running model).
	prov := &blockingProvider{started: make(chan struct{})}

	cfg := config.DefaultConfig()
	cfg.Provider.Default = "fake"
	output := make(chan OutputEvent, 256)
	a := NewAgent(st, prov, tools.NewRegistry(), cfg, sid, output, nil)
	a.SetModel("m")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.PromptWithContext(ctx, "hello") }()

	// Wait for the stream to start, then cancel.
	select {
	case <-prov.started:
	case <-time.After(2 * time.Second):
		t.Fatal("provider did not start")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Send did not return after cancel")
	}

	msgs, _ := st.GetMessages(sid)
	// The user message should remain (not soft-deleted).
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message (user only) after cancel, got %d", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("message role = %q, want user", msgs[0].Role)
	}
}

// TestInteg_CancelDuringToolKeepsResults verifies that cancelling during tool
// execution stores tool_result with an error (no orphaned tool_use).
func TestInteg_CancelDuringToolKeepsResults(t *testing.T) {
	dir := testutil.TempDir(t)
	st, err := store.Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	sid := "itest-cancel-tool"
	st.CreateSession(&store.Session{ID: sid, Cwd: dir, Provider: "fake", Model: "m", CreatedAt: time.Now().Unix()})

	prov := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	first, _ := provider.FakeToolCallResponse("wait", map[string]string{"x": "y"}, "done")
	prov.SetResponses([][]provider.StreamEvent{first})

	reg := tools.NewRegistry()
	toolStarted := make(chan struct{})
	reg.Register(blockingTool{started: toolStarted})

	cfg := config.DefaultConfig()
	cfg.Provider.Default = "fake"
	output := make(chan OutputEvent, 256)
	a := NewAgent(st, prov, reg, cfg, sid, output, nil)
	a.SetModel("m")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.PromptWithContext(ctx, "use tool") }()

	select {
	case <-toolStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("tool did not start")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Send did not return")
	}

	msgs, _ := st.GetMessages(sid)
	// Expected: user, assistant(tool_use), tool_result(error) — all kept.
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages after cancel, got %d", len(msgs))
	}
	if msgs[2].Role != "tool" {
		t.Errorf("msg[2] role = %q, want tool", msgs[2].Role)
	}
	if !hasBlock(msgs[2], "tool_result") {
		t.Error("tool_result block missing")
	}
}

// TestInteg_EmptyResponseHandling verifies the agent handles a model that
// returns zero content (no text, no thinking, no tool calls).
func TestInteg_EmptyResponseHandling(t *testing.T) {
	e := newIntegEnv(t, [][]provider.StreamEvent{
		{provider.StreamEvent{Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 5, OutputTokens: 0}}},
	})

	err := e.agent.PromptWithContext(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for empty response")
	}

	msgs := e.msgs()
	// Only the user message should be stored — no empty assistant message.
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message (user only), got %d", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("msg[0] role = %q, want user", msgs[0].Role)
	}
}

// TestInteg_EffortPropagation verifies the configured effort is sent in the
// provider request.
func TestInteg_EffortPropagation(t *testing.T) {
	e := newIntegEnv(t, [][]provider.StreamEvent{
		provider.FakeTextResponse("ok", nil),
	})

	e.agent.SetEffort("high")
	e.send("test")

	req := e.prov.LastRequest()
	if req == nil {
		t.Fatal("no request captured")
	}
	if req.Effort != "high" {
		t.Errorf("request effort = %q, want high", req.Effort)
	}
}

// TestInteg_RedactedThinking verifies redacted thinking blocks are stored.
func TestInteg_RedactedThinking(t *testing.T) {
	e := newIntegEnv(t, [][]provider.StreamEvent{
		provider.FakeRedactedThinkingResponse("Here is my answer.", nil),
	})

	events := e.send("question")

	var sawRedacted bool
	for _, ev := range events {
		if ev.Type == OutputThinking && ev.ThinkingRedacted {
			sawRedacted = true
		}
	}
	if !sawRedacted {
		t.Error("no redacted thinking event received")
	}

	msgs := e.msgs()
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if !hasBlock(msgs[1], "thinking") {
		t.Error("assistant message missing thinking block")
	}
}

// TestInteg_ApiCallRecorded verifies that API calls are recorded for cost tracking.
func TestInteg_ApiCallRecorded(t *testing.T) {
	e := newIntegEnv(t, [][]provider.StreamEvent{
		provider.FakeTextResponse("answer", &provider.Usage{
			InputTokens:  100,
			OutputTokens: 50,
		}),
	})

	e.send("test")

	call, err := e.store.GetLastAPICall(e.sid)
	if err != nil {
		t.Fatalf("GetAPICalls: %v", err)
	}
	if call == nil {
		t.Fatal("no API call recorded")
	}
	if call.InputTokens != 100 || call.OutputTokens != 50 {
		t.Errorf("usage = %+v, want {100, 50}", call)
	}
}

// TestInteg_AllFilesUnderTmp verifies that the test framework creates all
// files under /tmp (no drive wear on the working directory).
func TestInteg_AllFilesUnderTmp(t *testing.T) {
	dir := testutil.TempDir(t)
	st, err := store.Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	st.Close()

	// The DB file should exist under /tmp.
	if !strings.HasPrefix(dir, "/tmp/") {
		t.Errorf("dir %q is not under /tmp", dir)
	}
}

// TestInteg_MultiToolCallFlow verifies that a model requesting two tools in
// one turn executes both and then produces a final answer.
func TestInteg_MultiToolCallFlow(t *testing.T) {
	dir := testutil.TempDir(t)
	st, err := store.Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	sid := "itest-multi-tool"
	st.CreateSession(&store.Session{ID: sid, Cwd: dir, Provider: "fake", Model: "m", CreatedAt: time.Now().Unix()})

	prov := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	first, second := provider.FakeMultiToolCallResponse(
		"write", map[string]string{"path": "a.txt", "content": "alpha"},
		"write", map[string]string{"path": "b.txt", "content": "beta"},
		"Both files written.",
	)
	prov.SetResponses([][]provider.StreamEvent{first, second})

	reg := tools.NewRegistry()
	reg.Register(tools.NewWriteTool(dir, true, nil))
	reg.Register(tools.NewReadTool(dir, true, nil))

	cfg := config.DefaultConfig()
	cfg.Provider.Default = "fake"
	output := make(chan OutputEvent, 256)
	a := NewAgent(st, prov, reg, cfg, sid, output, nil)
	a.SetModel("m")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a.PromptWithContext(ctx, "write two files")

	msgs, _ := st.GetMessages(sid)
	// user, assistant(tool_use x2), tool_result x2, assistant(text) = 5
	if len(msgs) != 5 {
		t.Fatalf("expected 5 messages, got %d", len(msgs))
	}
	if !hasBlock(msgs[1], "tool_use") {
		t.Errorf("msg[1] missing tool_use: %q", msgs[1].Role)
	}
	if msgs[2].Role != "tool" || msgs[3].Role != "tool" {
		t.Errorf("expected two tool results, got %q and %q", msgs[2].Role, msgs[3].Role)
	}
	if msgText(msgs[4]) != "Both files written." {
		t.Errorf("final answer = %q", msgText(msgs[4]))
	}

	// Verify both files were written.
	r := tools.NewReadTool(dir, true, nil)
	res, _ := r.Execute(context.Background(), mustJSON(t, map[string]string{"path": "a.txt"}))
	if !strings.Contains(res.Content, "alpha") {
		t.Errorf("a.txt content = %q", res.Content)
	}
	res, _ = r.Execute(context.Background(), mustJSON(t, map[string]string{"path": "b.txt"}))
	if !strings.Contains(res.Content, "beta") {
		t.Errorf("b.txt content = %q", res.Content)
	}
}

// TestInteg_ToolErrorHandling verifies that a tool error is stored in the
// tool_result block and the model receives it for the next turn.
func TestInteg_ToolErrorHandling(t *testing.T) {
	dir := testutil.TempDir(t)
	st, err := store.Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	sid := "itest-tool-err"
	st.CreateSession(&store.Session{ID: sid, Cwd: dir, Provider: "fake", Model: "m", CreatedAt: time.Now().Unix()})

	prov := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	input, _ := json.Marshal(map[string]string{"path": "missing.txt"})
	first := []provider.StreamEvent{
		{Type: provider.EventToolUseStart, ToolCall: &provider.ToolCall{ID: "call_1", Name: "read", Input: input}},
		{Type: provider.EventToolUseStop, ToolCall: &provider.ToolCall{ID: "call_1", Name: "read", Input: input}},
		{Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 10, OutputTokens: 5}},
	}
	second := provider.FakeTextResponse("The file was missing.", nil)
	prov.SetResponses([][]provider.StreamEvent{first, second})

	reg := tools.NewRegistry()
	reg.Register(tools.NewReadTool(dir, true, nil))

	cfg := config.DefaultConfig()
	cfg.Provider.Default = "fake"
	output := make(chan OutputEvent, 256)
	a := NewAgent(st, prov, reg, cfg, sid, output, nil)
	a.SetModel("m")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a.PromptWithContext(ctx, "read a file")

	msgs, _ := st.GetMessages(sid)
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(msgs))
	}
	if msgs[2].Role != "tool" {
		t.Errorf("msg[2] role = %q, want tool", msgs[2].Role)
	}
	if !hasBlock(msgs[2], "tool_result") {
		t.Error("tool_result block missing")
	}
	var blocks []struct {
		Type      string `json:"type"`
		ToolIsError bool `json:"tool_is_error"`
	}
	_ = json.Unmarshal([]byte(msgs[2].Content), &blocks)
	var foundError bool
	for _, b := range blocks {
		if b.Type == "tool_result" && b.ToolIsError {
			foundError = true
		}
	}
	if !foundError {
		t.Errorf("tool_result should be marked as error: %q", msgs[2].Content)
	}
}

// TestInteg_ProviderError verifies that an EventError from the provider is
// returned as an error and no empty assistant message is stored.
func TestInteg_ProviderError(t *testing.T) {
	e := newIntegEnv(t, [][]provider.StreamEvent{
		provider.FakeErrorResponse(errors.New("model overloaded")),
	})

	err := e.agent.PromptWithContext(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for provider failure")
	}
	if !strings.Contains(err.Error(), "model overloaded") {
		t.Errorf("error = %q, want containing 'model overloaded'", err.Error())
	}

	msgs := e.msgs()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message (user only), got %d", len(msgs))
	}
}

// TestInteg_CostTracking verifies session cost and total cost are updated
// after API calls.
func TestInteg_CostTracking(t *testing.T) {
	e := newIntegEnv(t, [][]provider.StreamEvent{
		provider.FakeTextResponse("answer 1", &provider.Usage{InputTokens: 100, OutputTokens: 50}),
		provider.FakeTextResponse("answer 2", &provider.Usage{InputTokens: 200, OutputTokens: 100}),
	})
	// Configure pricing for the fake model so cost is non-zero.
	e.agent.Config().Pricing["fake"] = map[string]config.Pricing{
		"test-model": {InputPerMTok: 1.0, OutputPerMTok: 2.0},
	}

	e.send("q1")
	e.send("q2")

	sessionCost, err := e.store.GetSessionCost(e.sid)
	if err != nil {
		t.Fatalf("GetSessionCost: %v", err)
	}
	totalCost, err := e.store.GetTotalCost()
	if err != nil {
		t.Fatalf("GetTotalCost: %v", err)
	}
	if sessionCost != totalCost {
		t.Errorf("sessionCost=%v != totalCost=%v", sessionCost, totalCost)
	}
	if sessionCost <= 0 {
		t.Errorf("expected positive cost, got %v", sessionCost)
	}
}

// TestInteg_SwitchSessionEffort verifies that switching the model validates
// the current effort against the new model's supported levels.
func TestInteg_SwitchSessionEffort(t *testing.T) {
	dir := testutil.TempDir(t)
	st, err := store.Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	sid := "itest-effort-switch"
	st.CreateSession(&store.Session{ID: sid, Cwd: dir, Provider: "fake", Model: "m", CreatedAt: time.Now().Unix()})

	// Configure fake model settings: supports only "high" and "max".
	provider.KnownModels["fake/m"] = provider.ModelSettings{
		ContextWindow:  8192,
		SupportsEffort: true,
		EffortLevels:   []string{"high", "max"},
	}
	defer delete(provider.KnownModels, "fake/m")

	prov := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	cfg := config.DefaultConfig()
	cfg.Provider.Default = "fake"
	output := make(chan OutputEvent, 256)
	a := NewAgent(st, prov, nil, cfg, sid, output, nil)

	// Select model "m"; default effort "medium" is not supported by fake/m
	// (only high/max), so it should be clamped to "high".
	a.SetModel("m")
	if got := a.Effort(); got != "high" {
		t.Errorf("effort after SetModel(m) = %q, want high", got)
	}

	// Set effort to "low" (unsupported), re-select model -> clamped to "high".
	a.SetEffort("low")
	a.SetModel("m")
	if got := a.Effort(); got != "high" {
		t.Errorf("effort after SetModel = %q, want high", got)
	}
}

func mustJSON(t *testing.T, v interface{}) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("mustJSON: %v", err)
	}
	return b
}
