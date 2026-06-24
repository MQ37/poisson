package tui

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"poisson/internal/agent"
)

// --- Helpers -----------------------------------------------------------

// newTestTUI creates a TUI with a bytes.Buffer as its writer and a nil agent,
// suitable for testing rendering and logic without a terminal.
func newTestTUI() *TUI {
	buf := &bytes.Buffer{}
	t := &TUI{
		outputChan: make(chan agent.OutputEvent, 64),
		history:    []string{},
		histIdx:    -1,
		sessionID:  "abc123def456",
		writer:     buf,
	}
	return t
}

func (t *TUI) output() string {
	return t.writer.(*bytes.Buffer).String()
}

// --- @file expansion tests ---------------------------------------------

func TestExpandAtFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hello.go")
	content := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	input := "Review this: @" + path
	got, err := expandAtFiles(input)
	if err != nil {
		t.Fatalf("expandAtFiles: %v", err)
	}

	if !strings.Contains(got, "```") {
		t.Errorf("expected fenced code block, got: %q", got)
	}
	if !strings.Contains(got, content) {
		t.Errorf("expected file contents in output, got: %q", got)
	}
	if !strings.Contains(got, "Review this:") {
		t.Errorf("expected surrounding text preserved, got: %q", got)
	}
}

func TestExpandAtFilesMissingFile(t *testing.T) {
	_, err := expandAtFiles("@/nonexistent/path/file.go")
	if err == nil {
		t.Error("expected error for missing file, got nil")
	}
}

func TestExpandAtFilesNoMatch(t *testing.T) {
	input := "just some text with no file refs"
	got, err := expandAtFiles(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != input {
		t.Errorf("expected unchanged input, got %q", got)
	}
}

func TestExpandAtFilesFenceEscalation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested.md")
	content := "```\ncode block inside\n```\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := expandAtFiles("@" + path)
	if err != nil {
		t.Fatal(err)
	}
	// The outer fence should be longer than ``` to avoid breaking.
	// Both the opening and closing fence use 4+ backticks (2 occurrences).
	if n := strings.Count(got, "````"); n != 2 {
		t.Errorf("expected two 4-backtick fences, got %d occurrences in: %q", n, got)
	}
}

// --- Slash command tests -----------------------------------------------

func TestHandleSlashCommandQuit(t *testing.T) {
	tui := newTestTUI()
	err := tui.handleSlashCommand("/quit")
	if err != errQuit {
		t.Errorf("expected errQuit, got %v", err)
	}
	if !strings.Contains(tui.output(), "bye") {
		t.Errorf("expected 'bye' in output, got %q", tui.output())
	}
}

func TestHandleSlashCommandClear(t *testing.T) {
	tui := newTestTUI()
	err := tui.handleSlashCommand("/clear")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(tui.output(), "\x1b[2J") {
		t.Errorf("expected ANSI clear screen sequence, got %q", tui.output())
	}
}

func TestHandleSlashCommandHelp(t *testing.T) {
	tui := newTestTUI()
	err := tui.handleSlashCommand("/help")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := tui.output()
	for _, cmd := range []string{"/help", "/quit", "/clear", "/new", "/resume",
		"/sessions", "/search", "/fork", "/undo", "/compact", "/model",
		"/reload", "/cost"} {
		if !strings.Contains(out, cmd) {
			t.Errorf("expected %s in help output, got %q", cmd, out)
		}
	}
}

func TestHandleSlashCommandStub(t *testing.T) {
	// /compact is the only remaining stub.
	tui := newTestTUI()
	err := tui.handleSlashCommand("/compact")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(tui.output(), "not yet available") {
		t.Errorf("expected 'not yet available', got %q", tui.output())
	}
}

func TestHandleSlashCommandUnknown(t *testing.T) {
	tui := newTestTUI()
	err := tui.handleSlashCommand("/bogus")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(tui.output(), "unknown command") {
		t.Errorf("expected 'unknown command', got %q", tui.output())
	}
}

func TestSlashComplete(t *testing.T) {
	tui := newTestTUI()
	tests := []struct {
		input string
		want  string
	}{
		{"/q", "/quit "},         // unique match → append space
		{"/hel", "/help "},       // unique match
		{"/se", "/se"},           // /sessions and /search → common prefix only
		{"/xyz", "/xyz"},         // no match → unchanged
		{"/", "/"},               // all match → common prefix "/"
		{"/new arg", "/new arg"}, // has space → no completion
	}
	for _, tt := range tests {
		got := tui.slashComplete(tt.input)
		if got != tt.want {
			t.Errorf("slashComplete(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// --- History tests -----------------------------------------------------

func TestHistoryNavigation(t *testing.T) {
	tui := newTestTUI()
	tui.history = []string{"first", "second", "third"}

	// Start: histIdx == len(history) (pointing past the end)
	tui.histIdx = len(tui.history)

	// Up → most recent ("third")
	tui.navigateHistory(-1)
	if tui.input != "third" {
		t.Errorf("up: expected 'third', got %q", tui.input)
	}

	// Up again → "second"
	tui.navigateHistory(-1)
	if tui.input != "second" {
		t.Errorf("up: expected 'second', got %q", tui.input)
	}

	// Up again → "first"
	tui.navigateHistory(-1)
	if tui.input != "first" {
		t.Errorf("up: expected 'first', got %q", tui.input)
	}

	// Up at oldest → stays "first"
	tui.navigateHistory(-1)
	if tui.input != "first" {
		t.Errorf("up at oldest: expected 'first', got %q", tui.input)
	}

	// Down → "second"
	tui.navigateHistory(1)
	if tui.input != "second" {
		t.Errorf("down: expected 'second', got %q", tui.input)
	}

	// Down → "third"
	tui.navigateHistory(1)
	if tui.input != "third" {
		t.Errorf("down: expected 'third', got %q", tui.input)
	}

	// Down past newest → empty
	tui.navigateHistory(1)
	if tui.input != "" {
		t.Errorf("down past newest: expected '', got %q", tui.input)
	}
}

func TestHistoryEmpty(t *testing.T) {
	tui := newTestTUI()
	tui.navigateHistory(-1)
	if tui.input != "" {
		t.Errorf("empty history up: expected '', got %q", tui.input)
	}
}

// --- renderEvent tests -------------------------------------------------

func TestRenderEventText(t *testing.T) {
	ev := agent.OutputEvent{Type: agent.OutputText, Text: "Hello "}
	got := renderEventString(ev)
	if got != "Hello " {
		t.Errorf("text: expected 'Hello ', got %q", got)
	}
}

func TestRenderEventTextUsesCRLF(t *testing.T) {
	ev := agent.OutputEvent{Type: agent.OutputText, Text: "one\ntwo\nthree"}
	got := renderEventString(ev)
	if got != "one\r\ntwo\r\nthree" {
		t.Errorf("text newline normalization = %q", got)
	}
}

func TestRenderEventToolStart(t *testing.T) {
	ev := agent.OutputEvent{
		Type:      agent.OutputToolStart,
		ToolName:  "read",
		ToolInput: []byte(`{"path":"main.go"}`),
	}
	got := renderEventString(ev)
	if !strings.Contains(got, "[read]") {
		t.Errorf("tool_start: expected [read], got %q", got)
	}
	if !strings.Contains(got, "working") {
		t.Errorf("tool_start: expected spinner, got %q", got)
	}
}

func TestRenderEventToolResultSuccess(t *testing.T) {
	ev := agent.OutputEvent{
		Type:              agent.OutputToolResult,
		ToolResultContent: "file contents here",
		ToolError:         "",
	}
	got := renderEventString(ev)
	if !strings.Contains(got, "✓") {
		t.Errorf("tool_result: expected ✓, got %q", got)
	}
	if !strings.Contains(got, "file contents here") {
		t.Errorf("tool_result: expected content, got %q", got)
	}
}

func TestRenderEventToolResultError(t *testing.T) {
	ev := agent.OutputEvent{
		Type:      agent.OutputToolResult,
		ToolError: "permission denied",
	}
	got := renderEventString(ev)
	if !strings.Contains(got, "✗") {
		t.Errorf("tool_result error: expected ✗, got %q", got)
	}
	if !strings.Contains(got, "permission denied") {
		t.Errorf("tool_result error: expected error text, got %q", got)
	}
}

func TestRenderEventToolResultTruncation(t *testing.T) {
	long := strings.Repeat("x", 300)
	ev := agent.OutputEvent{
		Type:              agent.OutputToolResult,
		ToolResultContent: long,
	}
	got := renderEventString(ev)
	if !strings.Contains(got, "...") {
		t.Errorf("tool_result: expected truncation, got len=%d", len(got))
	}
}

func TestRenderEventError(t *testing.T) {
	ev := agent.OutputEvent{Type: agent.OutputError, Text: "something went wrong"}
	got := renderEventString(ev)
	if !strings.Contains(got, "error:") {
		t.Errorf("error: expected 'error:', got %q", got)
	}
	if !strings.Contains(got, "something went wrong") {
		t.Errorf("error: expected error text, got %q", got)
	}
}

func TestRenderEventCompacting(t *testing.T) {
	ev := agent.OutputEvent{Type: agent.OutputCompacting}
	got := renderEventString(ev)
	if !strings.Contains(got, "compacting") {
		t.Errorf("compacting: expected 'compacting', got %q", got)
	}
}

func TestRenderEventApproval(t *testing.T) {
	ev := agent.OutputEvent{Type: agent.OutputApproval, ToolName: "bash"}
	got := renderEventString(ev)
	if !strings.Contains(got, "approval") {
		t.Errorf("approval: expected 'approval', got %q", got)
	}
	if !strings.Contains(got, "bash") {
		t.Errorf("approval: expected tool name, got %q", got)
	}
}

// --- renderStatusBar tests ---------------------------------------------

func TestRenderStatusBarFormat(t *testing.T) {
	ev := agent.OutputEvent{
		Type:          agent.OutputStatus,
		ContextPct:    42.3,
		ContextTokens: 12847,
		ContextWindow: 30400,
		Cost:          0.0124,
		Model:         "anthropic/claude-sonnet-4",
	}
	got := renderStatusBarString(ev, "abc123def456")

	// Expected: [abc123] ctx: 42.3% (12,847/30,400) | $0.0124 | anthropic/claude-sonnet-4
	if !strings.Contains(got, "[abc123]") {
		t.Errorf("status bar: expected short session ID, got %q", got)
	}
	if !strings.Contains(got, "42.3%") {
		t.Errorf("status bar: expected ctx pct, got %q", got)
	}
	if !strings.Contains(got, "12,847") {
		t.Errorf("status bar: expected comma-formatted tokens, got %q", got)
	}
	if !strings.Contains(got, "30,400") {
		t.Errorf("status bar: expected comma-formatted window, got %q", got)
	}
	if !strings.Contains(got, "$0.0124") {
		t.Errorf("status bar: expected cost, got %q", got)
	}
	if !strings.Contains(got, "anthropic/claude-sonnet-4") {
		t.Errorf("status bar: expected model, got %q", got)
	}
}

func TestRenderStatusBarWarning(t *testing.T) {
	ev := agent.OutputEvent{
		Type:          agent.OutputStatus,
		ContextPct:    80.0,
		ContextTokens: 24000,
		ContextWindow: 30000,
		Cost:          0.05,
		Model:         "ollama/gemma:12b",
	}
	got := renderStatusBarString(ev, "sess1")
	if !strings.Contains(got, "⚠") {
		t.Errorf("status bar: expected ⚠ at >75%% ctx, got %q", got)
	}
}

func TestRenderStatusBarNoWarning(t *testing.T) {
	ev := agent.OutputEvent{
		Type:          agent.OutputStatus,
		ContextPct:    50.0,
		ContextTokens: 15000,
		ContextWindow: 30000,
		Cost:          0.01,
		Model:         "ollama/gemma:12b",
	}
	got := renderStatusBarString(ev, "sess1")
	if strings.Contains(got, "⚠") {
		t.Errorf("status bar: expected no ⚠ at 50%% ctx, got %q", got)
	}
}

// --- formatNum tests ---------------------------------------------------

func TestFormatNum(t *testing.T) {
	tests := []struct {
		input int
		want  string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1,000"},
		{12847, "12,847"},
		{30400, "30,400"},
		{1000000, "1,000,000"},
	}
	for _, tt := range tests {
		got := formatNum(tt.input)
		if got != tt.want {
			t.Errorf("formatNum(%d) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// --- Drain output test (with fake agent) -------------------------------

func TestDrainOutput(t *testing.T) {
	tui := newTestTUI()
	tui.outputChan = make(chan agent.OutputEvent, 16)

	// Simulate events already in the buffer, ending with "done".
	tui.outputChan <- agent.OutputEvent{Type: agent.OutputText, Text: "Hello "}
	tui.outputChan <- agent.OutputEvent{Type: agent.OutputText, Text: "world"}
	tui.outputChan <- agent.OutputEvent{Type: agent.OutputToolStart, ToolName: "read"}
	tui.outputChan <- agent.OutputEvent{Type: agent.OutputDone}

	promptDone := make(chan error, 1)
	promptDone <- nil
	if err := tui.drainOutput(promptDone, nil); err != nil {
		t.Fatalf("drainOutput: %v", err)
	}

	out := tui.output()
	if !strings.Contains(out, "Hello world") {
		t.Errorf("expected 'Hello world' in output, got %q", out)
	}
	if !strings.Contains(out, "[read]") {
		t.Errorf("expected tool start in output, got %q", out)
	}
}

// --- ToolInputPreview tests --------------------------------------------

func TestToolInputPreview(t *testing.T) {
	tests := []struct {
		tool  string
		input []byte
		want  string
	}{
		{"read", nil, "..."},
		{"read", []byte(""), "..."},
		{"read", []byte(`{"path":"main.go"}`), "main.go"},
		{"bash", []byte(`{"command":"echo hello"}`), "$ echo hello"},
		{"write", []byte(`{"path":"/tmp/a.txt","content":"hello"}`), "/tmp/a.txt (5 bytes)"},
		{"unknown", []byte(strings.Repeat("x", 100)), strings.Repeat("x", 80) + "..."},
	}
	for _, tt := range tests {
		got := toolInputPreview(tt.tool, tt.input)
		if got != tt.want {
			t.Errorf("toolInputPreview(%q, %q) = %q, want %q", tt.tool, tt.input, got, tt.want)
		}
	}
}
func TestDeleteWordBackward(t *testing.T) {
	tui := newTestTUI()
	tui.input = "hello world foo"
	tui.cursorPos = len(tui.input)

	tui.deleteWordBackward()

	if tui.input != "hello world " {
		t.Errorf("after deleteWordBackward: input=%q, want %q", tui.input, "hello world ")
	}
	if tui.cursorPos != len("hello world ") {
		t.Errorf("cursorPos=%d, want %d", tui.cursorPos, len("hello world "))
	}
}

func TestDeleteWordBackwardWithWhitespace(t *testing.T) {
	tui := newTestTUI()
	tui.input = "hello world   foo"
	tui.cursorPos = len(tui.input)

	tui.deleteWordBackward()

	// Should skip trailing spaces before "foo" then delete "foo"
	if tui.input != "hello world   " {
		t.Errorf("after deleteWordBackward with whitespace: input=%q, want %q", tui.input, "hello world   ")
	}
}

func TestDeleteWordBackwardAtStart(t *testing.T) {
	tui := newTestTUI()
	tui.input = "hello"
	tui.cursorPos = 0

	tui.deleteWordBackward()

	if tui.input != "hello" {
		t.Errorf("at start: input=%q, want unchanged", tui.input)
	}
}

func TestDeleteWordBackwardSingleWord(t *testing.T) {
	tui := newTestTUI()
	tui.input = "hello"
	tui.cursorPos = len(tui.input)

	tui.deleteWordBackward()

	if tui.input != "" {
		t.Errorf("single word: input=%q, want empty", tui.input)
	}
	if tui.cursorPos != 0 {
		t.Errorf("single word: cursorPos=%d, want 0", tui.cursorPos)
	}
}

func TestFeedPasteMultiChar(t *testing.T) {
	tui := newTestTUI()
	// Simulate a paste of multiple characters arriving in one read.
	if err := tui.feed([]byte("hello world")); err != nil {
		t.Fatalf("feed: %v", err)
	}
	if tui.input != "hello world" {
		t.Errorf("input = %q, want %q", tui.input, "hello world")
	}
	if tui.cursorPos != len("hello world") {
		t.Errorf("cursorPos = %d, want %d", tui.cursorPos, len("hello world"))
	}
}

func TestFeedUTF8(t *testing.T) {
	tui := newTestTUI()
	// Multi-byte UTF-8 should be inserted correctly.
	if err := tui.feed([]byte("héllo → 世界")); err != nil {
		t.Fatalf("feed: %v", err)
	}
	if tui.input != "héllo → 世界" {
		t.Errorf("input = %q, want %q", tui.input, "héllo → 世界")
	}
}

func TestFeedBracketedPaste(t *testing.T) {
	tui := newTestTUI()
	// Bracketed paste: ESC[200~ <content> ESC[201~
	data := append([]byte("\x1b[200~"), []byte("pasted text with\nnewline")...)
	data = append(data, []byte("\x1b[201~")...)
	if err := tui.feed(data); err != nil {
		t.Fatalf("feed: %v", err)
	}
	if tui.input != "pasted text with\nnewline" {
		t.Errorf("input = %q, want %q", tui.input, "pasted text with\nnewline")
	}
}

func TestFeedBracketedPasteSpanningReads(t *testing.T) {
	tui := newTestTUI()
	// First read: paste start + partial content (no end marker).
	if err := tui.feed([]byte("\x1b[200~part one ")); err != nil {
		t.Fatalf("feed 1: %v", err)
	}
	if !tui.pasting {
		t.Error("should be in pasting state after partial paste")
	}
	// Second read: rest of content + end marker.
	if err := tui.feed([]byte("part two\x1b[201~")); err != nil {
		t.Fatalf("feed 2: %v", err)
	}
	if tui.pasting {
		t.Error("should have exited pasting state")
	}
	if tui.input != "part one part two" {
		t.Errorf("input = %q, want %q", tui.input, "part one part two")
	}
}

func TestFeedBackspace(t *testing.T) {
	tui := newTestTUI()
	tui.feed([]byte("abc"))
	tui.feed([]byte{127}) // backspace
	if tui.input != "ab" {
		t.Errorf("input = %q, want %q", tui.input, "ab")
	}
}

func TestFeedArrowKeysNoCorruption(t *testing.T) {
	tui := newTestTUI()
	tui.feed([]byte("abc"))
	// Left arrow should move cursor, not corrupt input.
	tui.feed([]byte("\x1b[D"))
	if tui.input != "abc" {
		t.Errorf("input = %q, want abc (unchanged by arrow)", tui.input)
	}
	if tui.cursorPos != 2 {
		t.Errorf("cursorPos = %d, want 2 (moved left one)", tui.cursorPos)
	}
}

func TestRenderToolResultSanitizesControls(t *testing.T) {
	ev := agent.OutputEvent{
		Type:              agent.OutputToolResult,
		ToolResultContent: "before\x1b[2Jafter\nnext",
	}
	got := renderEventString(ev)
	if strings.Contains(got, "\x1b") {
		t.Fatalf("rendered terminal escape: %q", got)
	}
	if strings.Contains(got, "\nnext") {
		t.Fatalf("rendered raw newline inside preview: %q", got)
	}
	if !strings.Contains(got, "before?[2Jafter next") {
		t.Fatalf("unexpected sanitized preview: %q", got)
	}
}

func TestToolInputPreviewSanitizesControls(t *testing.T) {
	got := toolInputPreview("read", []byte("{\"path\":\"\x1b[2Jx\"}"))
	if strings.Contains(got, "\x1b") {
		t.Fatalf("preview contains ESC: %q", got)
	}
	if !strings.Contains(got, "?[2J") {
		t.Fatalf("escape was not made visible: %q", got)
	}
}

func TestToolResultPreviewBashUsesStdout(t *testing.T) {
	got := toolResultPreview("bash", `{"stdout":"hello\nworld","stderr":"","exitCode":0}`)
	if got != "hello world" {
		t.Fatalf("bash preview = %q", got)
	}
}

func TestVisibleInputShort(t *testing.T) {
	visible, cursor := visibleInput("hello", len("hello"), 10)
	if visible != "hello" || cursor != 5 {
		t.Fatalf("visible=%q cursor=%d, want hello/5", visible, cursor)
	}
}

func TestVisibleInputLongAtEnd(t *testing.T) {
	visible, cursor := visibleInput("abcdefghijklmnopqrstuvwxyz", len("abcdefghijklmnopqrstuvwxyz"), 10)
	if visible != "…rstuvwxyz" {
		t.Fatalf("visible=%q, want ellipsis + tail", visible)
	}
	if cursor != 10 {
		t.Fatalf("cursor=%d, want 10", cursor)
	}
}

func TestVisibleInputLongNearStart(t *testing.T) {
	visible, cursor := visibleInput("abcdefghijklmnopqrstuvwxyz", 3, 10)
	if visible != "abcdefghi…" {
		t.Fatalf("visible=%q, want head + ellipsis", visible)
	}
	if cursor != 3 {
		t.Fatalf("cursor=%d, want 3", cursor)
	}
}

func TestVisibleInputSanitizesNewlines(t *testing.T) {
	visible, _ := visibleInput("hello\nworld", len("hello\n"), 20)
	if visible != "hello↵world" {
		t.Fatalf("visible=%q, want newline marker", visible)
	}
}

func TestRefreshLineDoesNotPrintPastViewport(t *testing.T) {
	tui := newTestTUI()
	tui.input = strings.Repeat("x", 200)
	tui.cursorPos = len(tui.input)
	tui.refreshLine()
	out := tui.output()
	if strings.Contains(out, strings.Repeat("x", 100)) {
		t.Fatalf("refresh printed unbounded input: %q", out)
	}
	if !strings.Contains(out, "…") {
		t.Fatalf("refresh missing ellipsis: %q", out)
	}
}

func TestIdleCtrlCClearsInput(t *testing.T) {
	tui := newTestTUI()
	if err := tui.feed([]byte("hello")); err != nil {
		t.Fatalf("feed: %v", err)
	}
	if err := tui.feed([]byte{3}); err != nil {
		t.Fatalf("ctrl-c: %v", err)
	}
	if tui.input != "" || tui.cursorPos != 0 {
		t.Fatalf("input=%q cursor=%d, want cleared", tui.input, tui.cursorPos)
	}
}

func TestIdleCtrlCRequiresSecondPressToExit(t *testing.T) {
	tui := newTestTUI()
	if err := tui.feed([]byte{3}); err != nil {
		t.Fatalf("first ctrl-c returned %v, want nil", err)
	}
	if !strings.Contains(tui.output(), "Ctrl+C again to exit") {
		t.Fatalf("missing exit warning: %q", tui.output())
	}
	if err := tui.feed([]byte{3}); err != errQuit {
		t.Fatalf("second ctrl-c = %v, want errQuit", err)
	}
}

func TestRunningCtrlCCancelsThenExits(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer r.Close()
	defer w.Close()
	tui := newTestTUI()
	tui.fd = int(r.Fd())
	if err := syscall.SetNonblock(tui.fd, true); err != nil {
		t.Fatalf("set nonblock: %v", err)
	}
	defer syscall.SetNonblock(tui.fd, false)

	cancelled := false
	cancel := func() { cancelled = true }
	if _, err := w.Write([]byte{3}); err != nil {
		t.Fatalf("write ctrl-c: %v", err)
	}
	wasCancelled, err := tui.pollRunningCtrlC(cancel, false)
	if err != nil {
		t.Fatalf("first poll: %v", err)
	}
	if !wasCancelled || !cancelled {
		t.Fatalf("cancelled state = %v/%v, want true/true", wasCancelled, cancelled)
	}
	if !strings.Contains(tui.output(), "cancelled") {
		t.Fatalf("missing cancelled message: %q", tui.output())
	}

	if _, err := w.Write([]byte{3}); err != nil {
		t.Fatalf("write second ctrl-c: %v", err)
	}
	_, err = tui.pollRunningCtrlC(cancel, true)
	if err != errQuit {
		t.Fatalf("second poll = %v, want errQuit", err)
	}
}

func TestApproveAllowDeny(t *testing.T) {
	for _, tc := range []struct {
		key  byte
		want bool
	}{{'a', true}, {'y', true}, {'d', false}, {'n', false}, {3, false}} {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("pipe: %v", err)
		}
		oldStdin := os.Stdin
		os.Stdin = r
		tui := newTestTUI()
		tui.fd = int(r.Fd())
		if _, err := w.Write([]byte{tc.key}); err != nil {
			t.Fatalf("write: %v", err)
		}
		got := tui.Approve("rm -rf x", "danger")
		os.Stdin = oldStdin
		r.Close()
		w.Close()
		if got != tc.want {
			t.Errorf("key %q: Approve = %v, want %v", tc.key, got, tc.want)
		}
	}
}

// TestApproveOnNonblockingFd reproduces the original bug: the Ctrl+C poller
// left stdin nonblocking, so the approval's blocking read returned EAGAIN and
// auto-denied. Approve must restore blocking mode and read the keypress.
func TestApproveOnNonblockingFd(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer r.Close()
	defer w.Close()
	oldStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = oldStdin }()

	tui := newTestTUI()
	tui.fd = int(r.Fd())
	tui.pollerActive.Store(true)
	if err := syscall.SetNonblock(tui.fd, true); err != nil {
		t.Fatalf("set nonblock: %v", err)
	}
	defer syscall.SetNonblock(tui.fd, false)

	if _, err := w.Write([]byte{'a'}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !tui.Approve("rm -rf x", "danger") {
		t.Fatal("expected allow on nonblocking fd (regression)")
	}
}
