package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/config"
	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/store"
	"github.com/mq37/poisson/internal/testutil"
	"github.com/mq37/poisson/internal/tools"
)

// barrierTool is a test tool whose Execute is fully controllable, used to prove
// the agent runs a turn's tool calls concurrently.
type barrierTool struct {
	name string
	run  func(ctx context.Context) (tools.ToolResult, error)
}

func (b barrierTool) Name() string            { return b.name }
func (b barrierTool) Description() string     { return b.name }
func (b barrierTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
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

// TestInteg_GatedToolsRunSequentially is the regression guard for the
// approval-ordering bug: two tool calls whose names are in
// approvalGatedTools (here "bash" and "create_sandbox", standing in for the
// reported bash-then-create_sandbox scenario) must never run concurrently —
// the second must not even start until the first's Execute has fully
// returned. Two approval-gated calls dispatched concurrently could
// otherwise show their human-approval prompts out of the model's
// submission order, since TUI.Approve's underlying lock has no FIFO
// guarantee.
func TestInteg_GatedToolsRunSequentially(t *testing.T) {
	st := newTestStore(t)
	sid := "itest-gated-seq"
	if err := st.CreateSession(&store.Session{ID: sid, Cwd: testutil.TempDir(t), Provider: "fake", Model: "m", CreatedAt: time.Now().Unix()}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	prov := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	prov.SetResponses(twoToolTurn("bash", "create_sandbox", "done"))

	var firstDone atomic.Bool
	secondSawFirstDone := make(chan bool, 1)

	reg := tools.NewRegistry()
	reg.Register(barrierTool{name: "bash", run: func(ctx context.Context) (tools.ToolResult, error) {
		time.Sleep(20 * time.Millisecond) // gives "create_sandbox" a real chance to start early if it wrongly could
		firstDone.Store(true)
		return tools.ToolResult{Content: "BASH_DONE"}, nil
	}})
	reg.Register(barrierTool{name: "create_sandbox", run: func(ctx context.Context) (tools.ToolResult, error) {
		secondSawFirstDone <- firstDone.Load()
		return tools.ToolResult{Content: "SANDBOX_DONE"}, nil
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

	select {
	case saw := <-secondSawFirstDone:
		if !saw {
			t.Error("create_sandbox started before bash finished — gated calls ran concurrently, want strictly sequential")
		}
	default:
		t.Fatal("create_sandbox never ran")
	}
}

// TestInteg_GatedToolCancelResolvesQueuedCalls is the regression guard for
// the orphaned-widget half of the same bug class: when the turn is
// cancelled while the first approval-gated call is still in flight, every
// other gated call still queued behind it (never even dispatched) must
// still get an immediate tool_result — not hang forever with no result
// ever emitted (which, for a subagent, means its TUI widget spins forever).
func TestInteg_GatedToolCancelResolvesQueuedCalls(t *testing.T) {
	st := newTestStore(t)
	sid := "itest-gated-cancel"
	if err := st.CreateSession(&store.Session{ID: sid, Cwd: testutil.TempDir(t), Provider: "fake", Model: "m", CreatedAt: time.Now().Unix()}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	arg := json.RawMessage(`{}`)
	turn := []provider.StreamEvent{
		{Type: provider.EventToolUseStart, ToolCall: &provider.ToolCall{ID: "call_1", Name: "bash", Input: arg}},
		{Type: provider.EventToolUseStop, ToolCall: &provider.ToolCall{ID: "call_1", Name: "bash", Input: arg}},
		{Type: provider.EventToolUseStart, ToolCall: &provider.ToolCall{ID: "call_2", Name: "edit", Input: arg}},
		{Type: provider.EventToolUseStop, ToolCall: &provider.ToolCall{ID: "call_2", Name: "edit", Input: arg}},
		{Type: provider.EventToolUseStart, ToolCall: &provider.ToolCall{ID: "call_3", Name: "write", Input: arg}},
		{Type: provider.EventToolUseStop, ToolCall: &provider.ToolCall{ID: "call_3", Name: "write", Input: arg}},
		{Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 10, OutputTokens: 5}},
	}
	prov := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	prov.SetResponses([][]provider.StreamEvent{turn})

	bashStarted := make(chan struct{})
	var editRan, writeRan atomic.Bool

	reg := tools.NewRegistry()
	reg.Register(barrierTool{name: "bash", run: func(ctx context.Context) (tools.ToolResult, error) {
		close(bashStarted)
		<-ctx.Done() // simulates a still-pending human approval when the user cancels
		return tools.ToolResult{}, ctx.Err()
	}})
	reg.Register(barrierTool{name: "edit", run: func(ctx context.Context) (tools.ToolResult, error) {
		editRan.Store(true)
		return tools.ToolResult{Content: "EDIT_DONE"}, nil
	}})
	reg.Register(barrierTool{name: "write", run: func(ctx context.Context) (tools.ToolResult, error) {
		writeRan.Store(true)
		return tools.ToolResult{Content: "WRITE_DONE"}, nil
	}})

	cfg := config.DefaultConfig()
	cfg.Provider.Default = "fake"
	a := NewAgent(st, prov, reg, cfg, sid, make(chan OutputEvent, 256), nil)
	a.SetModel("m")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.PromptWithContext(ctx, "run three") }()

	select {
	case <-bashStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("bash never started")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("PromptWithContext never returned after cancel — a queued gated call is hanging")
	}

	if editRan.Load() || writeRan.Load() {
		t.Error("edit/write ran after the turn was cancelled — should have been resolved as cancelled instead")
	}

	msgs, _ := st.GetMessages(sid)
	// user, assistant(tool_use x3), tool_result(bash), tool_result(edit), tool_result(write)
	if len(msgs) != 5 {
		t.Fatalf("expected 5 messages, got %d", len(msgs))
	}
	for i, want := range []string{"cancelled", "cancelled"} {
		msg := msgs[3+i]
		if msg.Role != "tool" || !strings.Contains(msg.Content, want) {
			t.Errorf("msg[%d] = %q, want a %q tool_result", 3+i, msg.Content, want)
		}
	}
}

// TestInteg_ToolDispatchCapsConcurrency is the regression guard for
// maxConcurrentToolCalls: a model response with more tool_use blocks than
// the cap must never run them all at once — each one still forks a real
// subprocess/connection for bash/grep/fetch, so an unbounded round is a
// local resource-exhaustion risk, not just a theoretical one.
func TestInteg_ToolDispatchCapsConcurrency(t *testing.T) {
	st := newTestStore(t)
	sid := "itest-cap"
	if err := st.CreateSession(&store.Session{ID: sid, Cwd: testutil.TempDir(t), Provider: "fake", Model: "m", CreatedAt: time.Now().Unix()}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	const numCalls = maxConcurrentToolCalls + 4
	arg := json.RawMessage(`{}`)
	var turn []provider.StreamEvent
	for i := 0; i < numCalls; i++ {
		id := fmt.Sprintf("call_%d", i)
		turn = append(turn,
			provider.StreamEvent{Type: provider.EventToolUseStart, ToolCall: &provider.ToolCall{ID: id, Name: "capped", Input: arg}},
			provider.StreamEvent{Type: provider.EventToolUseStop, ToolCall: &provider.ToolCall{ID: id, Name: "capped", Input: arg}},
		)
	}
	turn = append(turn, provider.StreamEvent{Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 10, OutputTokens: 5}})

	prov := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	prov.SetResponses([][]provider.StreamEvent{turn, provider.FakeTextResponse("done", nil)})

	release := make(chan struct{})
	var inFlight, maxInFlight int32
	reg := tools.NewRegistry()
	reg.Register(barrierTool{name: "capped", run: func(ctx context.Context) (tools.ToolResult, error) {
		n := atomic.AddInt32(&inFlight, 1)
		defer atomic.AddInt32(&inFlight, -1)
		for {
			m := atomic.LoadInt32(&maxInFlight)
			if n <= m || atomic.CompareAndSwapInt32(&maxInFlight, m, n) {
				break
			}
		}
		<-release
		return tools.ToolResult{Content: "ok"}, nil
	}})

	cfg := config.DefaultConfig()
	cfg.Provider.Default = "fake"
	a := NewAgent(st, prov, reg, cfg, sid, make(chan OutputEvent, 256), nil)
	a.SetModel("m")

	done := make(chan error, 1)
	go func() {
		done <- a.PromptWithContext(context.Background(), "run many")
	}()

	// Give every goroutine a chance to reach the barrier tool before
	// asserting the high-water mark — polling instead of a fixed sleep
	// since maxInFlight can still climb for a few more scheduler ticks.
	deadline := time.After(2 * time.Second)
	for {
		if atomic.LoadInt32(&inFlight) >= maxConcurrentToolCalls {
			break
		}
		select {
		case <-deadline:
			t.Fatal("never reached the concurrency cap — dispatch may be stuck")
		case <-time.After(5 * time.Millisecond):
		}
	}
	// One more scheduling window to let any goroutine past the cap prove it
	// can't also join in.
	time.Sleep(20 * time.Millisecond)
	if got := atomic.LoadInt32(&inFlight); got > maxConcurrentToolCalls {
		t.Fatalf("in-flight tool calls = %d, want <= %d (cap not enforced)", got, maxConcurrentToolCalls)
	}
	close(release)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("PromptWithContext: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("turn never completed after release")
	}
	if got := atomic.LoadInt32(&maxInFlight); got != maxConcurrentToolCalls {
		t.Errorf("max concurrent tool executions = %d, want exactly %d", got, maxConcurrentToolCalls)
	}
}

// TestInteg_BatchedSubagentGetsOwnStartAndDoneEvents is the regression guard
// for the fix that gives a subagent nested inside batch its own live TUI
// widget instead of being invisible until the whole batch call finishes:
// agent.go must pre-emit a synthetic OutputToolStart (ToolName="subagent")
// for each nested subagent call before dispatching the batch, and (via
// tools.BindBatchSubagentDone) a matching OutputToolResult once THAT call
// finishes — not only once, for the whole batch.
func TestInteg_BatchedSubagentGetsOwnStartAndDoneEvents(t *testing.T) {
	batchInput, err := json.Marshal(map[string]interface{}{
		"calls": []map[string]interface{}{
			{"tool": "subagent", "input": map[string]string{"task": "explore checkout"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	turn := []provider.StreamEvent{
		{Type: provider.EventToolUseStart, ToolCall: &provider.ToolCall{ID: "call_batch", Name: "batch", Input: batchInput}},
		{Type: provider.EventToolUseStop, ToolCall: &provider.ToolCall{ID: "call_batch", Name: "batch", Input: batchInput}},
		{Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 10, OutputTokens: 5}},
	}
	e := newIntegEnv(t, [][]provider.StreamEvent{turn, provider.FakeTextResponse("done", nil)})

	// A stub "subagent" tool stands in for the real SubagentTool (which
	// spawns an actual child process) — this test is only about agent.go's
	// event plumbing around batch, not subagent execution itself.
	e.reg.Register(barrierTool{name: "subagent", run: func(ctx context.Context) (tools.ToolResult, error) {
		return tools.ToolResult{Content: "child result"}, nil
	}})
	e.reg.Register(tools.NewBatchTool(e.reg))
	tools.BindBatchSubagentDone(e.reg, e.agent.CompleteBatchedSubagent)

	events := e.send("run it")

	wantID := tools.BatchCallID("call_batch", 0)
	var sawStart, sawDone bool
	startIdx, doneIdx := -1, -1
	for i, ev := range events {
		if ev.Type == OutputToolStart && ev.ToolName == "subagent" && ev.ToolCallID == wantID {
			sawStart, startIdx = true, i
		}
		if ev.Type == OutputToolResult && ev.ToolName == "subagent" && ev.ToolCallID == wantID {
			sawDone, doneIdx = true, i
			if ev.ToolResultContent != "child result" {
				t.Errorf("done event content = %q, want %q", ev.ToolResultContent, "child result")
			}
		}
	}
	if !sawStart {
		t.Fatal("missing synthetic OutputToolStart for the batched subagent call")
	}
	if !sawDone {
		t.Fatal("missing synthetic OutputToolResult for the batched subagent call")
	}
	if startIdx > doneIdx {
		t.Errorf("start event (idx %d) came after done event (idx %d)", startIdx, doneIdx)
	}
}

// TestInteg_PanickingToolDoesNotCrashTurn covers the crash traced back from a
// subagent session that died with a bare "EOF" reaching its parent, with zero
// information about why — one tool call panicked (registry.Execute, agent.go's
// dispatch goroutine, and the child process all had no recover anywhere on
// this path). A single tool panicking must not crash the whole turn, let
// alone the process: the panicking call becomes an ordinary error tool_result,
// its sibling in the same round still completes normally, and the agent
// keeps running to get the model's next response — exactly like any other
// tool returning an error, not a special/degraded path.
func TestInteg_PanickingToolDoesNotCrashTurn(t *testing.T) {
	st := newTestStore(t)
	sid := "itest-panic"
	if err := st.CreateSession(&store.Session{ID: sid, Cwd: testutil.TempDir(t), Provider: "fake", Model: "m", CreatedAt: time.Now().Unix()}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	prov := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	prov.SetResponses(twoToolTurn("panicky", "normal", "done"))

	reg := tools.NewRegistry()
	reg.Register(barrierTool{name: "panicky", run: func(ctx context.Context) (tools.ToolResult, error) {
		panic("simulated tool bug: nil pointer somewhere deep in a real tool")
	}})
	reg.Register(barrierTool{name: "normal", run: func(ctx context.Context) (tools.ToolResult, error) {
		return tools.ToolResult{Content: "NORMAL_DONE"}, nil
	}})

	cfg := config.DefaultConfig()
	cfg.Provider.Default = "fake"
	a := NewAgent(st, prov, reg, cfg, sid, make(chan OutputEvent, 256), nil)
	a.SetModel("m")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// The panic must never reach here as an actual Go panic — if recovery
	// failed anywhere on the path, this call itself would crash the test
	// process (an unrecovered panic in any goroutine kills the whole program,
	// not just the goroutine it started in).
	if err := a.PromptWithContext(ctx, "run both"); err != nil {
		t.Fatalf("PromptWithContext: %v (turn must complete despite the panicking tool)", err)
	}

	msgs, err := st.GetMessages(sid)
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	// user, assistant(tool_use x2), tool_result(panicky), tool_result(normal), assistant(text)
	if len(msgs) != 5 {
		t.Fatalf("expected 5 messages, got %d: %+v", len(msgs), msgs)
	}
	if msgs[2].Role != "tool" || !strings.Contains(msgs[2].Content, "panicked") ||
		!strings.Contains(msgs[2].Content, "simulated tool bug") {
		t.Errorf("msg[2] should be panicky's error result naming the panic, got %q", msgs[2].Content)
	}
	if !strings.Contains(msgs[2].Content, `"tool_is_error":true`) {
		t.Errorf("msg[2] must be flagged as an error tool_result, got %q", msgs[2].Content)
	}
	if msgs[3].Role != "tool" || !strings.Contains(msgs[3].Content, "NORMAL_DONE") {
		t.Errorf("msg[3] should be normal's successful result (sibling call in the same round must still complete), got %q", msgs[3].Content)
	}
	if msgs[4].Role != "assistant" || !strings.Contains(msgs[4].Content, "done") {
		t.Errorf("msg[4] should be the model's final answer after the tool round — proves the agent kept running, got %q", msgs[4].Content)
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
	t      *testing.T
	dir    string
	store  *store.Store
	prov   *provider.FakeProvider
	agent  *Agent
	sid    string
	output chan OutputEvent
	reg    *tools.Registry
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
	reg.Register(tools.NewReadTool(dir, alwaysApprove))
	reg.Register(tools.NewWriteTool(dir, alwaysApprove))
	reg.Register(tools.NewBashTool(dir, alwaysApprove))

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
	reg.Register(tools.NewWriteTool(dir, alwaysApprove))
	reg.Register(tools.NewReadTool(dir, alwaysApprove))

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
	r := tools.NewReadTool(dir, alwaysApprove)
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
	reg.Register(tools.NewWriteTool(dir, alwaysApprove))
	reg.Register(tools.NewReadTool(dir, alwaysApprove))

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

// partialThenBlockProvider streams one text delta, then blocks until ctx is
// cancelled — simulates the user cancelling mid-response (some content
// already streamed), unlike blockingProvider above which never emits
// anything before hanging.
type partialThenBlockProvider struct {
	started chan struct{}
}

func (p *partialThenBlockProvider) ID() string { return "fake" }
func (p *partialThenBlockProvider) Models() ([]provider.Model, error) {
	return []provider.Model{{ID: "m", ContextWindow: 8192}}, nil
}
func (p *partialThenBlockProvider) Stream(ctx context.Context, req *provider.Request) (<-chan provider.StreamEvent, error) {
	ch := make(chan provider.StreamEvent, 4)
	ch <- provider.StreamEvent{Type: provider.EventTextDelta, Text: "Here is a partial answer"}
	go func() {
		defer close(ch)
		close(p.started)
		<-ctx.Done()
	}()
	return ch, nil
}

// TestInteg_CancelMidTextPersistsPartialResponse is the reported live bug:
// cancelling while the assistant's text is still streaming kept the partial
// response visible in the current TUI's scrollback (it already streamed as
// OutputText events) but never stored it, so a follow-up message never sent
// it back to the model — the model would have no idea what it already said.
func TestInteg_CancelMidTextPersistsPartialResponse(t *testing.T) {
	dir := testutil.TempDir(t)
	st, err := store.Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	sid := "itest-cancel-partial"
	st.CreateSession(&store.Session{ID: sid, Cwd: dir, Provider: "fake", Model: "m", CreatedAt: time.Now().Unix()})

	prov := &partialThenBlockProvider{started: make(chan struct{})}
	cfg := config.DefaultConfig()
	cfg.Provider.Default = "fake"
	output := make(chan OutputEvent, 256)
	a := NewAgent(st, prov, tools.NewRegistry(), cfg, sid, output, nil)
	a.SetModel("m")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.PromptWithContext(ctx, "hello") }()

	select {
	case <-prov.started:
	case <-time.After(2 * time.Second):
		t.Fatal("provider did not stream")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Send did not return after cancel")
	}

	msgs, _ := st.GetMessages(sid)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (user + partial assistant) after cancel, got %d", len(msgs))
	}
	if msgs[1].Role != "assistant" {
		t.Fatalf("msg[1] role = %q, want assistant", msgs[1].Role)
	}
	if !hasBlock(msgs[1], "text") || !strings.Contains(msgs[1].Content, "Here is a partial answer") {
		t.Errorf("partial assistant content missing, got %q", msgs[1].Content)
	}

	// The next turn must actually send that partial text back to the provider.
	prov2 := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	prov2.SetResponses([][]provider.StreamEvent{provider.FakeTextResponse("continuing", nil)})
	a2 := NewAgent(st, prov2, tools.NewRegistry(), cfg, sid, output, nil)
	a2.SetModel("m")
	if err := a2.PromptWithContext(context.Background(), "keep going"); err != nil {
		t.Fatal(err)
	}
	req := prov2.LastRequest()
	var sawPartial bool
	for _, m := range req.Messages {
		for _, b := range m.Content {
			if b.Type == "text" && strings.Contains(b.Text, "Here is a partial answer") {
				sawPartial = true
			}
		}
	}
	if !sawPartial {
		t.Error("partial response from the cancelled turn was not sent to the provider on the next turn")
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
	reg.Register(tools.NewWriteTool(dir, alwaysApprove))
	reg.Register(tools.NewReadTool(dir, alwaysApprove))

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
	r := tools.NewReadTool(dir, alwaysApprove)
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
	reg.Register(tools.NewReadTool(dir, alwaysApprove))

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
		Type        string `json:"type"`
		ToolIsError bool   `json:"tool_is_error"`
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
