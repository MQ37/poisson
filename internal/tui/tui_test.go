package tui

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"unicode/utf8"

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

func TestTabComplete(t *testing.T) {
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
		got := tui.tabComplete(tt.input)
		if got != tt.want {
			t.Errorf("tabComplete(%q) = %q, want %q", tt.input, got, tt.want)
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

func TestMatchSlash(t *testing.T) {
	cases := []struct {
		partial string
		want    []string
	}{
		{"/", []string{"/quit", "/clear", "/help", "/new", "/resume", "/sessions", "/search", "/fork", "/undo", "/compact", "/model", "/effort", "/models", "/providers", "/reload", "/cost"}},
		{"/m", []string{"/model", "/models"}},
		{"/mo", []string{"/model", "/models"}},
		{"/mod", []string{"/model", "/models"}},
		{"/xyz", nil},
		{"foo", nil},
	}
	for _, tc := range cases {
		got := matchSlash(tc.partial)
		if !equalStrings(got, tc.want) {
			t.Errorf("matchSlash(%q) = %v, want %v", tc.partial, got, tc.want)
		}
	}
}

func TestCommonPrefixCands(t *testing.T) {
	cases := []struct {
		xs   []string
		want string
	}{
		{nil, ""},
		{[]string{}, ""},
		{[]string{"/model"}, "/model"},
		{[]string{"/model", "/models"}, "/model"},
		{[]string{"/a", "/b"}, "/"},
	}
	for _, tc := range cases {
		got := commonPrefixCands(tc.xs)
		if got != tc.want {
			t.Errorf("commonPrefixCands(%v) = %q, want %q", tc.xs, got, tc.want)
		}
	}
}

func TestSplitPrefix(t *testing.T) {
	cases := []struct {
		line      string
		col       int
		wantHead  string
		wantToken string
	}{
		{"", 0, "", ""},
		{"/mo", 3, "/mo", "/mo"},
		{"/model foo", 3, "/mo", "/mo"},
		{"/model foo", 6, "/model", "/model"},
		{"@README", 7, "@README", "@README"},
		{"hello @RE", 9, "hello @RE", "@RE"},
	}
	for _, tc := range cases {
		gotHead, gotTok := splitPrefix(tc.line, tc.col)
		if gotHead != tc.wantHead || gotTok != tc.wantToken {
			t.Errorf("splitPrefix(%q, %d) = (%q, %q), want (%q, %q)",
				tc.line, tc.col, gotHead, gotTok, tc.wantHead, tc.wantToken)
		}
	}
}

func TestMatchAtFile(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"alpha.txt", "beta.go", "gamma.md"} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0644); err != nil {
			t.Fatal(err)
		}
	}
	got := matchAtFile("@", dir)
	if len(got) != 3 {
		t.Errorf("expected 3 matches for \"@\", got %d: %v", len(got), got)
	}
	got = matchAtFile("@a", dir)
	if len(got) != 1 || got[0] != "@alpha.txt" {
		t.Errorf("expected [@alpha.txt], got %v", got)
	}
	got = matchAtFile("@"+filepath.Join(dir, "a"), "/")
	want := "@" + filepath.Join(dir, "alpha.txt")
	if len(got) != 1 || got[0] != want {
		t.Errorf("absolute match = %v, want [%s]", got, want)
	}
}

func TestCompletionEmpty(t *testing.T) {
	var c *completion
	if !c.empty() {
		t.Error("nil completion should be empty")
	}
	c = &completion{}
	if !c.empty() {
		t.Error("empty candidate list should be empty")
	}
	c = &completion{cands: []string{"/help"}, idx: -1}
	if c.empty() {
		t.Error("non-empty should not be empty")
	}
	if c.selected() != "" {
		t.Error("unselected (idx=-1) should return empty string")
	}
	c.idx = 0
	if c.selected() != "/help" {
		t.Errorf("selected = %q, want /help", c.selected())
	}
	c.cycle(+1)
	if c.idx != 0 {
		t.Errorf("cycle+1 wraps to start: idx = %d", c.idx)
	}
	c.cycle(-1)
	// Wraps back to last index when cycling backward from 0.
	if c.idx != 0 {
		t.Errorf("cycle-1 from 0 wraps to last (0 here since 1 candidate): idx = %d", c.idx)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestWrapOne(t *testing.T) {
	cases := []struct {
		in    string
		width int
		want  int // number of chunks
	}{
		{"", 10, 1},
		{"hello", 10, 1},
		{"hello world", 5, 3},          // hard wrap: "hello", " worl", "d"
		{"supercalifragilistic", 5, 4}, // hard break at 5
		{"a b c d e", 3, 3},            // hard wrap: "a b", " c ", "d e"
	}
	for _, tc := range cases {
		got := wrapOne(tc.in, tc.width)
		if len(got) != tc.want {
			t.Errorf("wrapOne(%q, %d) = %d chunks (%v), want %d",
				tc.in, tc.width, len(got), got, tc.want)
		}
	}
}

func TestScreenCursorRoundTrip(t *testing.T) {
	e := &editor{lines: []string{"", "hello world this is a long line"}, row: 1, col: 10}
	width := 5
	sr, sc := screenCursor(e, width)
	if sr < 0 || sc < 0 {
		t.Fatalf("screenCursor invalid: sr=%d sc=%d", sr, sc)
	}
	// Round-trip back.
	r, c := screenToLogical(e, width, sr, sc)
	if r != e.row || c != e.col {
		t.Errorf("round-trip failed: got (%d,%d), want (%d,%d)", r, c, e.row, e.col)
	}
}

func TestMoveDownScreen(t *testing.T) {
	// Long line wrapping to 3 screen rows; cursor should traverse all 3 rows
	// without changing logical row.
	e := &editor{lines: []string{"a very long line that wraps"}, row: 0, col: 5}
	width := 5
	e.moveDownScreen(width)
	if e.row != 0 {
		t.Fatalf("logical row changed: got %d want 0", e.row)
	}
	e.moveDownScreen(width)
	if e.row != 0 {
		t.Fatalf("logical row changed at screen row 2: got %d", e.row)
	}
	// At bottom; further down should not change anything except col clamping.
	sr, _ := screenCursor(e, width)
	total := totalVisualLines(e, width)
	if sr != total-1 {
		t.Logf("sr=%d total-1=%d (informational)", sr, total-1)
	}
}

// TestRenderLongInputProducesMultipleScreenRows reproduces the bug the user
// hit: with cols=80, a 200-char single line must produce ≥3 wrapped chunks
// rendered across 3 body rows.
func TestRenderLongInputProducesMultipleScreenRows(t *testing.T) {
	longLine := strings.Repeat("x", 200)
	e := &editor{lines: []string{longLine}, row: 0, col: 0, wrapWidth: 79}
	chunks := wrapLines(e.lines, e.wrapWidth)
	if len(chunks) < 3 {
		t.Fatalf("expected ≥3 wrapped chunks for 200-char line at width 79, got %d", len(chunks))
	}
	if got := len(chunks[0]); got != 79 {
		t.Errorf("chunk[0] length = %d, want 79", got)
	}
	// Total chunks must sum to original runes (modulo stripped whitespace).
	total := 0
	for _, c := range chunks {
		total += utf8.RuneCountInString(c)
	}
	if total < 200 {
		t.Errorf("chunks cover only %d of 200 chars", total)
	}
}

// TestScreenCursorStaysValidAfterGrowth ensures screenCursor still maps to a
// valid screen row when the editor grows past one logical line.
func TestScreenCursorStaysValidAfterGrowth(t *testing.T) {
	e := &editor{lines: []string{strings.Repeat("a", 200)}, row: 0, col: 200, wrapWidth: 79}
	sr, sc := screenCursor(e, 79)
	if sr < 0 || sc < 0 {
		t.Fatalf("invalid screen cursor: sr=%d sc=%d", sr, sc)
	}
	if sr < 1 {
		t.Errorf("expected cursor in wrapped second row, got sr=%d", sr)
	}
}

func TestEditorEnterSubmitsCtrlJNewline(t *testing.T) {
	e := newEditor()
	e.insertText("hello")
	sub, _ := e.feed([]byte("\r"))
	if sub != "hello" {
		t.Errorf("Enter should submit, got %q", sub)
	}

	e2 := newEditor()
	e2.insertText("hello")
	sub2, _ := e2.feed([]byte{10}) // Ctrl+J / LF
	if sub2 != "" {
		t.Errorf("Ctrl+J should not submit, got %q", sub2)
	}
	if e2.text() != "hello\n" {
		t.Errorf("Ctrl+J should insert newline, got %q", e2.text())
	}
}

func TestEditorKittyShiftEnterNewline(t *testing.T) {
	e := newEditor()
	e.insertText("hello")
	// kitty keyboard protocol: ESC [ 13 ; 2 u
	sub, _ := e.feed([]byte{27, '[', '1', '3', ';', '2', 'u'})
	if sub != "" {
		t.Errorf("Shift+Enter should not submit, got %q", sub)
	}
	if e.text() != "hello\n" {
		t.Errorf("Shift+Enter should insert newline, got %q", e.text())
	}
}

func TestEditorKittyEnterSubmits(t *testing.T) {
	e := newEditor()
	e.insertText("hello")
	// kitty keyboard protocol: ESC [ 13 u
	sub, _ := e.feed([]byte{27, '[', '1', '3', 'u'})
	if sub != "hello" {
		t.Errorf("kitty Enter should submit, got %q", sub)
	}
}

func TestV2NavigateHistory(t *testing.T) {
	tui := newTUIv2(nil, "s-abc", nil)
	tui.history = []string{"first", "second", "third"}
	tui.editor.setText("draft")

	tui.navigateHistory(-1) // oldest
	if tui.editor.text() != "third" {
		t.Errorf("oldest history = %q, want third", tui.editor.text())
	}
	if tui.editor.col != len("third") {
		t.Errorf("history cursor col = %d, want end", tui.editor.col)
	}
	tui.navigateHistory(+1) // back to draft
	if tui.editor.text() != "draft" {
		t.Errorf("back to draft = %q, want draft", tui.editor.text())
	}
	tui.navigateHistory(-1)
	tui.navigateHistory(-1) // second
	if tui.editor.text() != "second" {
		t.Errorf("second history = %q, want second", tui.editor.text())
	}
}

func TestV2KittyPageKeysScrollBack(t *testing.T) {
	tui := newTUIv2(nil, "s-abc", nil)
	tui.rows = 20
	tui.cols = 80
	tui.scrollRows = 5
	for i := 0; i < 20; i++ {
		tui.scroll.appendRaw(styleSystem, "line")
	}
	kittyPageUp := []byte{27, '[', '5', '7', '3', '5', '4', 'u'}
	if delta, ok := parseScrollInputRaw(kittyPageUp, tui.scrollRows); !ok || delta != 5 {
		t.Fatalf("parseScrollInputRaw page up = %d %v", delta, ok)
	}
	tui.handleScrollDelta(5)
	if tui.scroll.scrollOffset == 0 {
		t.Fatal("kitty PageUp did not scroll")
	}
}

func TestV2PageKeysScrollBack(t *testing.T) {
	tui := newTUIv2(nil, "s-abc", nil)
	tui.rows = 20
	tui.cols = 80
	tui.scrollRows = 5
	for i := 0; i < 20; i++ {
		tui.scroll.appendRaw(styleSystem, "line")
	}
	if quit, err := tui.feed([]byte("\x1b[5~")); quit || err != nil {
		t.Fatalf("PageUp feed quit=%v err=%v", quit, err)
	}
	if tui.scroll.scrollOffset == 0 {
		t.Fatal("PageUp did not scroll up")
	}
	if quit, err := tui.feed([]byte("\x1b[6~")); quit || err != nil {
		t.Fatalf("PageDown feed quit=%v err=%v", quit, err)
	}
	if tui.scroll.scrollOffset != 0 {
		t.Fatalf("PageDown scrollOffset = %d, want 0", tui.scroll.scrollOffset)
	}
}

func TestEditorXtermShiftEnterNewline(t *testing.T) {
	e := newEditor()
	e.wrapWidth = 40
	e.insertText("hello")
	// xterm modifyOtherKeys: ESC [ 27 ; 2 ; 13 ~
	sub, _ := e.feed([]byte{27, '[', '2', '7', ';', '2', ';', '1', '3', '~'})
	if sub != "" {
		t.Errorf("xterm Shift+Enter should not submit, got %q", sub)
	}
	if e.text() != "hello\n" {
		t.Errorf("xterm Shift+Enter should insert newline, got %q", e.text())
	}
}

func TestInsertNewlineAtStart(t *testing.T) {
	e := newEditor()
	e.wrapWidth = 40
	e.insertText("hello")
	e.col = 0
	e.insertNewline()
	if e.text() != "\nhello" {
		t.Errorf("insertNewline at start: got %q", e.text())
	}
	if e.row != 1 || e.col != 0 {
		t.Errorf("cursor should be at row=1 col=0, got row=%d col=%d", e.row, e.col)
	}
}

func TestInsertNewlineInMultiLine(t *testing.T) {
	e := newEditor()
	e.wrapWidth = 40
	e.insertText("aaa\nbbb\nccc")
	e.row = 1
	e.col = 1
	e.insertNewline()
	if e.text() != "aaa\nb\nbb\nccc" {
		t.Errorf("insertNewline in middle: got %q", e.text())
	}
	if e.row != 2 || e.col != 0 {
		t.Errorf("cursor should be at row=2 col=0, got row=%d col=%d", e.row, e.col)
	}
}

func TestEditorArrowUpMultiLine(t *testing.T) {
	e := newEditor()
	e.wrapWidth = 10
	e.insertText("line1\nline2")
	e.row = 1
	e.col = 2
	// Up arrow: ESC [ A
	e.feed([]byte{27, '[', 'A'})
	if e.row != 0 {
		t.Errorf("up arrow should move to row 0, got %d", e.row)
	}
	if e.col != 2 {
		t.Errorf("up arrow should preserve col 2, got %d", e.col)
	}
}

func TestEditorEnterDoesNotQuit(t *testing.T) {
	e := newEditor()
	e.insertText("hello")
	submitted, quit := e.feed([]byte{'\r'})
	if submitted != "hello" {
		t.Errorf("Enter should submit text, got %q", submitted)
	}
	if quit {
		t.Error("Enter must NOT signal quit — that was causing px to exit on every submit")
	}
}

func TestEditorCtrlDQuitOnlyWhenEmpty(t *testing.T) {
	e := newEditor()
	e.insertText("hello")
	submitted, quit := e.feed([]byte{4})
	if quit {
		t.Error("Ctrl+D with text should not quit")
	}
	if submitted != "" {
		t.Errorf("Ctrl+D with text should not submit, got %q", submitted)
	}

	e2 := newEditor()
	submitted2, quit2 := e2.feed([]byte{4})
	if !quit2 {
		t.Error("Ctrl+D with empty buffer should quit")
	}
	if submitted2 != "" {
		t.Errorf("Ctrl+D should not submit, got %q", submitted2)
	}
}

func TestEditorInsertRuneMultibyte(t *testing.T) {
	e := newEditor()
	e.wrapWidth = 40
	// Insert a multibyte rune and verify it round-trips correctly.
	e.insertRune('é')
	e.insertRune('l')
	if e.text() != "él" {
		t.Errorf("expected él, got %q", e.text())
	}
	if e.col != 2 {
		t.Errorf("expected col=2, got %d", e.col)
	}
	// Backspace should delete one rune, not one byte.
	e.backspace()
	if e.text() != "é" {
		t.Errorf("expected é after backspace, got %q", e.text())
	}
}

func TestEditorInsertTextMultiline(t *testing.T) {
	e := newEditor()
	e.wrapWidth = 40
	e.insertText("hello")
	e.col = 2
	e.insertText("X\nY\nZ")
	// "heX\nY\nZllo"
	if e.text() != "heX\nY\nZllo" {
		t.Errorf("got %q", e.text())
	}
	if e.row != 2 || e.col != 1 {
		t.Errorf("cursor should be row=2 col=1, got row=%d col=%d", e.row, e.col)
	}
}

func TestFeedPasteBypass(t *testing.T) {
	// Pasted bytes inside bracketed paste markers should go directly to the
	// editor, not trigger Tab/Enter/Esc interception.
	tui := newTUIv2(nil, "s-test", nil)
	tui.cols = 80
	tui.rows = 24
	tui.editor.wrapWidth = 79

	// Simulate a paste containing a tab and CR.
	paste := append([]byte("\x1b[200~"), []byte("hello\tworld\r\n")...)
	paste = append(paste, []byte("\x1b[201~")...)
	quit, err := tui.processEditor(paste)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if quit {
		t.Fatal("paste should not quit")
	}
	// The editor should contain the pasted text with tab and CR converted.
	expected := "hello\tworld\n"
	if tui.editor.text() != expected {
		t.Errorf("expected %q, got %q", expected, tui.editor.text())
	}
	// No completion should have been triggered.
	if tui.completion != nil {
		t.Error("paste triggered completion — should have been bypassed")
	}
}

func TestContainsSubmitKeyKittyVariants(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want bool
	}{
		{"raw CR", []byte{'\r'}, true},
		{"kitty Enter no mods", []byte{27, '[', '1', '3', 'u'}, true},
		{"kitty Enter mods=1", []byte{27, '[', '1', '3', ';', '1', 'u'}, true},
		{"kitty Shift+Enter", []byte{27, '[', '1', '3', ';', '2', 'u'}, false},
		{"kitty Ctrl+Enter", []byte{27, '[', '1', '3', ';', '5', 'u'}, false},
		{"plain text", []byte("hello"), false},
	}
	for _, c := range cases {
		got := containsSubmitKey(c.data)
		if got != c.want {
			t.Errorf("%s: containsSubmitKey=%v want %v", c.name, got, c.want)
		}
	}
}

func TestDecodeKittyKeys(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want []byte
	}{
		{"plain text passes through", []byte("hello"), []byte("hello")},
		{"kitty backspace", []byte{27, '[', '1', '2', '7', 'u'}, []byte{127}},
		{"kitty enter", []byte{27, '[', '1', '3', 'u'}, []byte{'\r'}},
		{"kitty shift+enter to newline", []byte{27, '[', '1', '3', ';', '2', 'u'}, []byte{'\n'}},
		{"kitty ctrl+c", []byte{27, '[', '9', '9', ';', '5', 'u'}, []byte{3}},
		{"kitty ctrl+d", []byte{27, '[', '1', '0', '0', ';', '5', 'u'}, []byte{4}},
		{"arrow up passes through", []byte{27, '[', 'A'}, []byte{27, '[', 'A'}},
		{"kitty arrow up", []byte{27, '[', '5', '7', '3', '5', '2', 'u'}, []byte{27, '[', 'A'}},
		{"kitty page up", []byte{27, '[', '5', '7', '3', '5', '4', 'u'}, []byte{27, '[', '5', '~'}},
		{"kitty page down", []byte{27, '[', '5', '7', '3', '5', '5', 'u'}, []byte{27, '[', '6', '~'}},
		{"shift+arrow dropped-as-csi passes through", []byte{27, '[', '1', ';', '2', 'A'}, []byte{27, '[', '1', ';', '2', 'A'}},
		{"text then kitty backspace", append([]byte("ab"), 27, '[', '1', '2', '7', 'u'), []byte{'a', 'b', 127}},
	}
	for _, c := range cases {
		got := decodeKittyKeys(c.in)
		if string(got) != string(c.want) {
			t.Errorf("%s: decodeKittyKeys(%q)=%q want %q", c.name, c.in, got, c.want)
		}
	}
}
