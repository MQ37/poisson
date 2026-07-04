package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// =============================================================================
// TUI integration tests
//
// These tests wire a real Agent (with FakeProvider, real store + tool registry)
// to a real TUI and verify the UI state end-to-end. All file I/O is under /tmp
// via testutil.TempDir. No real API calls are made.
// =============================================================================

type tuiIntegEnv struct {
	t       *testing.T
	dir     string
	sid     string
	store   *store.Store
	prov    *provider.FakeProvider
	agent   *agent.Agent
	tui     *TUI
	reg     *tools.Registry
}

// newTUIIntegEnv creates an Agent + TUI pair sharing the same output channel.
func newTUIIntegEnv(t *testing.T, responses [][]provider.StreamEvent) *tuiIntegEnv {
	t.Helper()
	testutil.TempHome(t)
	dir := testutil.TempDir(t)
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	sid := "tui-integ-" + strings.ReplaceAll(t.Name(), "/", "-")
	cfg := config.DefaultConfig()
	cfg.Provider.Default = "fake"

	prov := provider.NewFakeProvider("fake", []provider.Model{
		{ID: "test-model", Name: "Test", ContextWindow: 8192},
	})
	if responses != nil {
		prov.SetResponses(responses)
	}

	reg := tools.NewRegistry()
	reg.Register(tools.NewReadTool(dir))
	reg.Register(tools.NewWriteTool(dir))
	reg.Register(tools.NewLsTool(dir))
	reg.Register(tools.NewGlobTool(dir))
	reg.Register(tools.NewBashTool(dir, true, alwaysApprove))

	if err := st.CreateSession(&store.Session{
		ID:        sid,
		Cwd:       dir,
		Provider:  "fake",
		Model:     "test-model",
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	output := make(chan agent.OutputEvent, 256)
	a := agent.NewAgent(st, prov, reg, cfg, sid, output, alwaysApprove)
	a.SetModel("test-model")

	tui := NewTUI(a, sid, output)
	tui.rows = 24
	tui.cols = 80
	tui.scrollRows = 20
	tui.writer = &bytes.Buffer{}
	tui.recomputeLayout()

	return &tuiIntegEnv{
		t:     t,
		dir:   dir,
		store: st,
		prov:  prov,
		agent: a,
		tui:   tui,
	}
}

// alwaysApprove passes every bash command without UI interaction.
func alwaysApprove(_, _, _ string) bool { return true }

// runTurn sends a prompt through the agent and applies all resulting events
// into the TUI synchronously. This mirrors TUI.submit() without the terminal
// I/O goroutines, making it deterministic for tests.
func (e *tuiIntegEnv) runTurn(msg string) {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Mirror what TUI.submit() does: append the user message to the scrollback
	// before starting the agent turn.
	e.tui.mu.Lock()
	e.tui.scroll.scrollToBottom()
	e.tui.scroll.append(StyledLine{Style: styleUser, Text: msg})
	e.tui.status.Thinking = true
	e.tui.dirty.markStatus()
	e.tui.mu.Unlock()

	err := e.agent.PromptWithContext(ctx, msg)
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		e.t.Fatalf("PromptWithContext: %v", err)
	}

	// Drain all events produced by the turn and apply them to the TUI.
	for {
		select {
		case ev := <-e.tui.output:
			e.tui.mu.Lock()
			e.tui.handleEvent(ev)
			e.tui.markAfterEvent(ev)
			e.tui.mu.Unlock()
		default:
			e.tui.mu.Lock()
			e.tui.status.Thinking = false
			e.tui.mu.Unlock()
			return
		}
	}
}

// render returns the current TUI screen as plain text (ANSI stripped).
func (e *tuiIntegEnv) render() string {
	e.t.Helper()
	// Both recomputeLayout and paint acquire t.mu internally; do not wrap
	// them in another Lock/Unlock to avoid re-entrant deadlock.
	e.tui.recomputeLayout()
	e.tui.dirty.markFull()
	e.tui.paint(e.tui.dirty.consume())
	buf, ok := e.tui.writer.(*bytes.Buffer)
	if !ok {
		e.t.Fatal("writer is not bytes.Buffer")
	}
	return stripANSI(buf.String())
}

// scrollText returns the concatenated raw text of all scrollback blocks.
func (e *tuiIntegEnv) scrollText() string {
	e.tui.mu.Lock()
	defer e.tui.mu.Unlock()
	var parts []string
	for i := 0; i < e.tui.scroll.blockCount(); i++ {
		parts = append(parts, e.tui.scroll.blockRaw(i))
	}
	return strings.Join(parts, "\n")
}

// blockMeta returns the metadata of scrollback block i.
func (e *tuiIntegEnv) blockMeta(i int) *BlockMeta {
	e.tui.mu.Lock()
	defer e.tui.mu.Unlock()
	if i < 0 || i >= e.tui.scroll.blockCount() {
		return nil
	}
	return &e.tui.scroll.blocks[i].meta
}

// =============================================================================
// Tests
// =============================================================================

// TestTUIInteg_SimpleTextConversation verifies that a plain text response is
// rendered as user + assistant blocks and appears in the final screen buffer.
func TestTUIInteg_SimpleTextConversation(t *testing.T) {
	e := newTUIIntegEnv(t, [][]provider.StreamEvent{
		provider.FakeTextResponse("Hello! I can help with that.", nil),
	})
	e.runTurn("hi there")

	if e.tui.scroll.blockCount() != 2 {
		t.Fatalf("expected 2 scrollback blocks, got %d", e.tui.scroll.blockCount())
	}
	if !strings.Contains(e.scrollText(), "Hello! I can help with that.") {
		t.Errorf("assistant text not in scrollback: %q", e.scrollText())
	}

	screen := e.render()
	if !strings.Contains(screen, "hi there") {
		t.Errorf("user message not rendered: %q", screen)
	}
	if !strings.Contains(screen, "Hello! I can help with that.") {
		t.Errorf("assistant message not rendered: %q", screen)
	}
	if !strings.Contains(screen, "fake/test-model") {
		t.Errorf("status bar missing model label: %q", screen)
	}
}

// TestTUIInteg_ThinkingBlocksRendered verifies that reasoning content creates
// a collapsed thinking block in the scrollback and renders the "thinking" header.
func TestTUIInteg_ThinkingBlocksRendered(t *testing.T) {
	e := newTUIIntegEnv(t, [][]provider.StreamEvent{
		provider.FakeThinkingTextResponse(
			"The user said hello. I should respond warmly.",
			"Hello! Great to meet you.",
			nil,
		),
	})
	e.runTurn("hello")

	// user + thinking + assistant text = 3 blocks; after OutputDone the thinking
	// block is collapsed and rendered as a one-line header.
	if e.tui.scroll.blockCount() != 3 {
		t.Fatalf("expected 3 blocks, got %d", e.tui.scroll.blockCount())
	}
	meta := e.blockMeta(1)
	if meta == nil {
		t.Fatal("no metadata for thinking block")
	}
	if !meta.Collapsed {
		t.Error("thinking block should be collapsed after OutputDone")
	}

	screen := e.render()
	if !strings.Contains(screen, "thinking") {
		t.Errorf("rendered output missing thinking header: %q", screen)
	}
	if !strings.Contains(screen, "Hello! Great to meet you.") {
		t.Errorf("rendered output missing assistant text: %q", screen)
	}
}

// TestTUIInteg_ThinkingCollapsedAfterDone verifies that after OutputDone the
// thinking block is collapsed (not expanded) and the header is still visible.
func TestTUIInteg_ThinkingCollapsedAfterDone(t *testing.T) {
	e := newTUIIntegEnv(t, [][]provider.StreamEvent{
		provider.FakeThinkingTextResponse("Some reasoning", "Final answer.", nil),
	})
	e.runTurn("hello")

	// After runTurn, all events including OutputDone have been processed.
	meta := e.blockMeta(1)
	if meta == nil {
		t.Fatal("no metadata for assistant block")
	}
	if !meta.Collapsed {
		t.Error("thinking block should be collapsed after OutputDone")
	}
	if meta.Streaming {
		t.Error("thinking block should not be streaming after OutputDone")
	}
}

// TestTUIInteg_ToolCallFlow verifies the full tool-call UI: tool_use block,
// tool_result block, and final text all appear in the scrollback.
func TestTUIInteg_ToolCallFlow(t *testing.T) {
	e := newTUIIntegEnv(t, nil) // responses set below
	first, second := provider.FakeToolCallResponse("write", map[string]string{
		"path":    "output.txt",
		"content": "hello world",
	}, "I wrote the file.")
	e.prov.SetResponses([][]provider.StreamEvent{first, second})

	e.runTurn("write a file")

	// user + assistant text + tool_call block + final assistant text = 4 blocks.
	if e.tui.scroll.blockCount() != 4 {
		t.Fatalf("expected 4 scrollback blocks, got %d", e.tui.scroll.blockCount())
	}
	scroll := e.scrollText()
	if !strings.Contains(scroll, "write") {
		t.Errorf("tool name not in scrollback: %q", scroll)
	}
	if !strings.Contains(scroll, "I wrote the file.") {
		t.Errorf("final answer not in scrollback: %q", scroll)
	}

	// Verify the file was actually written.
	r := tools.NewReadTool(e.dir)
	res, _ := r.Execute(context.Background(), mustJSONTUI(t, map[string]string{"path": "output.txt"}))
	if !strings.Contains(res.Content, "hello world") {
		t.Errorf("file content = %q, want 'hello world'", res.Content)
	}
}

// TestTUIInteg_ToolErrorShown verifies that a tool returning an error produces
// a visible tool_result error in the scrollback.
func TestTUIInteg_ToolErrorShown(t *testing.T) {
	e := newTUIIntegEnv(t, nil)
	input, _ := json.Marshal(map[string]string{"path": "missing.txt"})
	first := []provider.StreamEvent{
		{Type: provider.EventToolUseStart, ToolCall: &provider.ToolCall{ID: "call_1", Name: "read", Input: input}},
		{Type: provider.EventToolUseStop, ToolCall: &provider.ToolCall{ID: "call_1", Name: "read", Input: input}},
		{Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 10, OutputTokens: 5}},
	}
	second := provider.FakeTextResponse("The file could not be read.", nil)
	e.prov.SetResponses([][]provider.StreamEvent{first, second})

	e.runTurn("read missing file")

	// Locate the tool card block and verify it carries the error.
	toolIdx := -1
	for i := 0; i < e.tui.scroll.blockCount(); i++ {
		if e.tui.scroll.blocks[i].kind == blockToolCall {
			toolIdx = i
			break
		}
	}
	if toolIdx == -1 {
		t.Fatal("no tool_call block in scrollback")
	}
	meta := e.blockMeta(toolIdx)
	if meta == nil || meta.ToolError == "" {
		t.Errorf("tool card should have ToolError set, meta=%+v", meta)
	}

	screen := e.render()
	if !strings.Contains(screen, "✗") {
		t.Errorf("rendered screen should show error marker ✗: %q", screen)
	}
	if !strings.Contains(screen, "The file could not be read.") {
		t.Errorf("final answer not rendered: %q", screen)
	}
}

// TestTUIInteg_CancelKeepsConversation verifies that cancelling a turn leaves
// the user message in the scrollback (no soft-delete) and renders a
// "cancelled" marker.
func TestTUIInteg_CancelKeepsConversation(t *testing.T) {
	e := newTUIIntegEnv(t, nil)

	slowProv := &slowProvider{started: make(chan struct{})}
	output := make(chan agent.OutputEvent, 256)
	a := agent.NewAgent(e.store, slowProv, e.reg, e.agent.Config(), e.sid, output, alwaysApprove)
	a.SetModel("test-model")
	e.agent = a
	e.tui = NewTUI(a, e.sid, output)
	e.tui.rows = 24
	e.tui.cols = 80
	e.tui.scrollRows = 20
	e.tui.writer = &bytes.Buffer{}
	e.tui.recomputeLayout()

	// Append user message to the scrollback, as TUI.submit() would do.
	e.tui.mu.Lock()
	e.tui.scroll.scrollToBottom()
	e.tui.scroll.append(StyledLine{Style: styleUser, Text: "hello"})
	e.tui.status.Thinking = true
	e.tui.mu.Unlock()

	turnCtx, turnCancel := context.WithCancel(context.Background())
	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		for ev := range output {
			e.tui.mu.Lock()
			e.tui.handleEvent(ev)
			e.tui.markAfterEvent(ev)
			e.tui.mu.Unlock()
		}
	}()

	turnDone := make(chan error, 1)
	go func() { turnDone <- e.agent.PromptWithContext(turnCtx, "hello") }()

	select {
	case <-slowProv.started:
	case <-time.After(2 * time.Second):
		t.Fatal("provider did not start")
	}
	// Set the same flag Ctrl+C sets so the TUI appends a "cancelled" marker.
	e.tui.mu.Lock()
	e.tui.turnCancelled = true
	e.tui.mu.Unlock()
	turnCancel()

	select {
	case <-turnDone:
	case <-time.After(5 * time.Second):
		t.Fatal("turn did not return after cancel")
	}

	// Close the output channel to stop the pump after draining final events.
	close(output)
	<-pumpDone

	e.tui.mu.Lock()
	e.tui.status.Thinking = false
	e.tui.mu.Unlock()

	scroll := e.scrollText()
	if !strings.Contains(scroll, "hello") {
		t.Errorf("user message should remain after cancel: %q", scroll)
	}
	screen := e.render()
	if !strings.Contains(screen, "cancelled") {
		t.Errorf("cancelled marker should be rendered: %q", screen)
	}
}

// TestTUIInteg_ProviderErrorRendered verifies that an EventError from the
// provider results in an error block in the scrollback and on screen.
func TestTUIInteg_ProviderErrorRendered(t *testing.T) {
	e := newTUIIntegEnv(t, [][]provider.StreamEvent{
		provider.FakeErrorResponse(errors.New("model overloaded")),
	})

	err := e.agent.PromptWithContext(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for provider failure")
	}

	// Pump any events that were emitted before the error.
	for {
		select {
		case ev := <-e.tui.output:
			e.tui.mu.Lock()
			e.tui.handleEvent(ev)
			e.tui.markAfterEvent(ev)
			e.tui.mu.Unlock()
		default:
			goto check
		}
	}
check:
	scroll := e.scrollText()
	if !strings.Contains(scroll, "model overloaded") {
		t.Errorf("error not shown in scrollback: %q", scroll)
	}
	screen := e.render()
	if !strings.Contains(screen, "model overloaded") {
		t.Errorf("error not rendered: %q", screen)
	}
}

// TestTUIInteg_MultiTurn verifies multiple back-to-back turns accumulate as
// separate blocks and the status bar reflects thinking/clear states.
func TestTUIInteg_MultiTurn(t *testing.T) {
	e := newTUIIntegEnv(t, [][]provider.StreamEvent{
		provider.FakeTextResponse("First.", nil),
		provider.FakeTextResponse("Second.", nil),
		provider.FakeTextResponse("Third.", nil),
	})

	e.runTurn("q1")
	e.runTurn("q2")
	e.runTurn("q3")

	if e.tui.scroll.blockCount() != 6 {
		t.Fatalf("expected 6 blocks, got %d", e.tui.scroll.blockCount())
	}
	scroll := e.scrollText()
	for _, want := range []string{"q1", "q2", "q3", "First.", "Second.", "Third."} {
		if !strings.Contains(scroll, want) {
			t.Errorf("scrollback missing %q: %q", want, scroll)
		}
	}

	// After all turns, thinking should be false.
	e.tui.mu.Lock()
	if e.tui.status.Thinking {
		t.Error("status.Thinking should be false after all turns complete")
	}
	e.tui.mu.Unlock()
}

// TestTUIInteg_StatusBarModelAndEffort verifies the header shows the model and
// that effort is propagated into the provider request.
func TestTUIInteg_StatusBarModelAndEffort(t *testing.T) {
	e := newTUIIntegEnv(t, [][]provider.StreamEvent{
		provider.FakeTextResponse("ok", nil),
	})
	e.agent.SetEffort("high")
	e.runTurn("test")

	screen := e.render()
	if !strings.Contains(screen, "fake/test-model") {
		t.Errorf("status bar missing model: %q", screen)
	}
	if !strings.Contains(screen, "high") {
		t.Errorf("status bar missing effort 'high': %q", screen)
	}

	req := e.prov.LastRequest()
	if req == nil {
		t.Fatal("no provider request captured")
	}
	if req.Effort != "high" {
		t.Errorf("request effort = %q, want high", req.Effort)
	}
}

// TestTUIInteg_ThinkingToggle verifies that Ctrl+T expands/collapses the
// last thinking block and the rendered output changes accordingly.
func TestTUIInteg_ThinkingToggle(t *testing.T) {
	e := newTUIIntegEnv(t, [][]provider.StreamEvent{
		provider.FakeThinkingTextResponse("Detailed reasoning", "Answer.", nil),
	})
	e.runTurn("hello")

	// After OutputDone the thinking block is collapsed.
	meta := e.blockMeta(1)
	if meta == nil || !meta.Collapsed {
		t.Fatal("thinking block should be collapsed before toggle")
	}

	collapsedScreen := e.render()
	if strings.Contains(collapsedScreen, "Detailed reasoning") {
		t.Errorf("reasoning text should be hidden when collapsed: %q", collapsedScreen)
	}

	e.tui.mu.Lock()
	toggled, _ := e.tui.scroll.toggleLastThinking()
	e.tui.mu.Unlock()
	if !toggled {
		t.Fatal("toggle should find a thinking block")
	}

	expandedScreen := e.render()
	if !strings.Contains(expandedScreen, "Detailed reasoning") {
		t.Errorf("reasoning text should be visible after toggle: %q", expandedScreen)
	}
}

// =============================================================================
// Helpers
// =============================================================================

// slowProvider blocks until its context is cancelled, simulating a long model
// response. Used only for cancel tests.
type slowProvider struct {
	started chan struct{}
}

func (p *slowProvider) ID() string { return "fake" }
func (p *slowProvider) Models() ([]provider.Model, error) {
	return []provider.Model{{ID: "test-model", ContextWindow: 8192}}, nil
}
func (p *slowProvider) Stream(ctx context.Context, _ *provider.Request) (<-chan provider.StreamEvent, error) {
	close(p.started)
	ch := make(chan provider.StreamEvent)
	go func() {
		defer close(ch)
		<-ctx.Done()
	}()
	return ch, nil
}

func mustJSONTUI(t *testing.T, v interface{}) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("mustJSON: %v", err)
	}
	return b
}
