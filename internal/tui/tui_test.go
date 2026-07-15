package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"poisson/internal/testutil"
)

// --- @file expansion tests ---------------------------------------------

func TestExpandAtFiles(t *testing.T) {
	dir := testutil.TempDir(t)
	path := filepath.Join(dir, "hello.go")
	content := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	input := "Review this: @" + path
	segs, err := expandAtFilesSegments(input)
	if err != nil {
		t.Fatalf("expandAtFilesSegments: %v", err)
	}
	got := segmentsText(segs)

	if !strings.Contains(got, "```") {
		t.Errorf("expected fenced code block, got: %q", got)
	}
	if !strings.Contains(got, content) {
		t.Errorf("expected file contents in output, got: %q", got)
	}
	if !strings.Contains(got, "Review this:") {
		t.Errorf("expected surrounding text preserved, got: %q", got)
	}

	foundRef := ""
	for _, s := range segs {
		if s.FileRef != "" {
			foundRef = s.FileRef
		}
	}
	if foundRef != path {
		t.Errorf("expected one segment with FileRef %q, got segments: %+v", path, segs)
	}
}

// A nonexistent @path is left as plain text instead of erroring — @word is a
// common false positive (e.g. "{@link Foo}" in a pasted JSDoc comment), and
// erroring the whole send on every such paste is worse than a no-op.
func TestExpandAtFilesMissingFileIsLeftAsPlainText(t *testing.T) {
	input := "see @/nonexistent/path/file.go for details"
	segs, err := expandAtFilesSegments(input)
	if err != nil {
		t.Fatalf("expandAtFilesSegments: %v", err)
	}
	if got := segmentsText(segs); got != input {
		t.Errorf("expected unchanged input, got %q", got)
	}
	for _, s := range segs {
		if s.FileRef != "" {
			t.Errorf("expected no FileRef segment for a missing file, got %+v", s)
		}
	}
}

func TestExpandAtFilesJSDocLinkTagPassesThrough(t *testing.T) {
	input := "cannot be judged until the client is known {@link TOOL_CLIENT_BLOCKLIST} rule"
	segs, err := expandAtFilesSegments(input)
	if err != nil {
		t.Fatalf("expandAtFilesSegments: %v", err)
	}
	if got := segmentsText(segs); got != input {
		t.Errorf("expected unchanged input, got %q", got)
	}
}

func TestExpandAtFilesNoMatch(t *testing.T) {
	input := "just some text with no file refs"
	segs, err := expandAtFilesSegments(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := segmentsText(segs); got != input {
		t.Errorf("expected unchanged input, got %q", got)
	}
}

func TestExpandAtFilesTooLarge(t *testing.T) {
	dir := testutil.TempDir(t)
	path := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(path, make([]byte, maxAtFileBytes+1), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := expandAtFilesSegments("@" + path)
	if err == nil {
		t.Fatal("expected error for oversized file")
	}
}

func TestExpandAtFilesBinary(t *testing.T) {
	dir := testutil.TempDir(t)
	path := filepath.Join(dir, "bin.dat")
	if err := os.WriteFile(path, []byte{0x00, 'a', 'b'}, 0644); err != nil {
		t.Fatal(err)
	}
	_, err := expandAtFilesSegments("@" + path)
	if err == nil {
		t.Fatal("expected error for binary file")
	}
}

func TestExpandAtFilesFenceEscalation(t *testing.T) {
	dir := testutil.TempDir(t)
	path := filepath.Join(dir, "nested.md")
	content := "```\ncode block inside\n```\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	segs, err := expandAtFilesSegments("@" + path)
	if err != nil {
		t.Fatal(err)
	}
	got := segmentsText(segs)
	// The outer fence should be longer than ``` to avoid breaking.
	// Both the opening and closing fence use 4+ backticks (2 occurrences).
	if n := strings.Count(got, "````"); n != 2 {
		t.Errorf("expected two 4-backtick fences, got %d occurrences in: %q", n, got)
	}
}

func TestExpandAtFilesDirectoryListing(t *testing.T) {
	dir := testutil.TempDir(t)
	// One level: two files, one subdir. The subdir has a nested file that must
	// NOT appear (listing is one level deep only).
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("y"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "worker"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "worker", "inner.js"), []byte("z"), 0644); err != nil {
		t.Fatal(err)
	}

	segs, err := expandAtFilesSegments("list @" + dir)
	if err != nil {
		t.Fatalf("expandAtFilesSegments on directory must not error: %v", err)
	}
	got := segmentsText(segs)
	if !strings.Contains(got, "(directory):") {
		t.Errorf("expected directory label, got: %q", got)
	}
	if !strings.Contains(got, "main.go") || !strings.Contains(got, "README.md") {
		t.Errorf("expected files listed, got: %q", got)
	}
	if !strings.Contains(got, "worker/") {
		t.Errorf("expected subdir suffixed with '/', got: %q", got)
	}
	if strings.Contains(got, "inner.js") {
		t.Errorf("listing must be one level deep only (no nested inner.js): %q", got)
	}
	if !strings.Contains(got, "list ") {
		t.Errorf("expected surrounding text preserved, got: %q", got)
	}
}

func TestExpandAtFilesFileStillInlines(t *testing.T) {
	// Regression guard: a plain file must still expand to fenced contents
	// (the directory branch must not swallow files).
	dir := testutil.TempDir(t)
	path := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0644); err != nil {
		t.Fatal(err)
	}
	segs, err := expandAtFilesSegments("@" + path)
	if err != nil {
		t.Fatalf("expandAtFilesSegments: %v", err)
	}
	got := segmentsText(segs)
	if !strings.Contains(got, "hello world") {
		t.Errorf("expected file contents inlined, got: %q", got)
	}
	if strings.Contains(got, "(directory):") {
		t.Errorf("file must not be treated as a directory: %q", got)
	}
}

func TestMatchSlash(t *testing.T) {
	cases := []struct {
		partial string
		want    []string
	}{
		{"/", []string{"/quit", "/clear", "/help", "/name", "/new", "/resume", "/sessions", "/search", "/compact", "/model", "/effort", "/providers", "/reload", "/cost", "/status", "/btw", "/openai-reset-usage"}},
		{"/m", []string{"/model"}},
		{"/mo", []string{"/model"}},
		{"/mod", []string{"/model"}},
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
		{[]string{"/model", "/mod"}, "/mod"},
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
func TestInputWrapWidthAccountsForPrompt(t *testing.T) {
	if got := inputWrapWidth(80); got != 77 {
		t.Fatalf("inputWrapWidth(80) = %d, want 77", got)
	}
}

func TestRenderInputScreenRowFitsTerminal(t *testing.T) {
	tui := &TUI{cols: 80}
	longLine := strings.Repeat("x", 120)
	screenLines := wrapLines([]string{longLine}, inputWrapWidth(80))
	row := tui.renderInputScreenRow(0, screenLines, 0, 10)
	if w := visibleWidth(row); w > 80 {
		t.Fatalf("first input row visible width %d exceeds cols 80", w)
	}
}

func TestRenderLongInputProducesMultipleScreenRows(t *testing.T) {
	longLine := strings.Repeat("x", 200)
	wrap := inputWrapWidth(80)
	e := &editor{lines: []string{longLine}, row: 0, col: 0, wrapWidth: wrap}
	chunks := wrapLines(e.lines, e.wrapWidth)
	if len(chunks) < 3 {
		t.Fatalf("expected ≥3 wrapped chunks for 200-char line at width %d, got %d", wrap, len(chunks))
	}
	if got := utf8.RuneCountInString(chunks[0]); got != wrap {
		t.Errorf("chunk[0] length = %d, want %d", got, wrap)
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

func TestArrowHistoryAtEditorEdges(t *testing.T) {
	tui := newTUI(nil, "s-abc", nil)
	tui.rows = 24
	tui.cols = 80
	tui.editor.wrapWidth = 79
	tui.history = []string{"first", "second"}
	tui.editor.setText("draft")
	tui.editor.col = len("draft")

	tui.feed([]byte{27, '[', 'A'}) // up at top of single line
	if tui.editor.text() != "second" {
		t.Errorf("up at top = %q, want second", tui.editor.text())
	}
}

// TestArrowMovesCursorInMultiLineInput is a regression test: the up/down
// dispatch in feedKey unconditionally returned after checking the history-nav
// boundary (editorAtScrollTop/Bottom), instead of only returning when history
// nav actually fired. That swallowed every up/down keystroke that wasn't
// already at the input's top/bottom edge — no history recall AND no cursor
// movement, i.e. up/down did nothing at all while editing a multi-line
// message. Drives through tui.feed (the real dispatch path), not editor.feed
// directly — TestEditorArrowUpMultiLine already covers the editor in
// isolation and did not catch this, since the bug was in feedKey's wrapper
// around it.
func TestArrowMovesCursorInMultiLineInput(t *testing.T) {
	tui := newTUI(nil, "s-abc", nil)
	tui.rows = 24
	tui.cols = 80
	tui.editor.wrapWidth = 79
	tui.editor.insertText("line1\nline2")
	tui.editor.row = 1
	tui.editor.col = 2

	tui.feed([]byte{27, '[', 'A'}) // up arrow, NOT at the input's top row
	if tui.editor.row != 0 {
		t.Fatalf("up arrow should move cursor to row 0, got row=%d (text unchanged: %v)", tui.editor.row, tui.editor.text() == "line1\nline2")
	}
	if tui.editor.col != 2 {
		t.Fatalf("up arrow should preserve col 2, got col=%d", tui.editor.col)
	}
	if tui.editor.text() != "line1\nline2" {
		t.Fatalf("up arrow must not trigger history nav here, text = %q", tui.editor.text())
	}

	tui.feed([]byte{27, '[', 'B'}) // down arrow, back to row 1
	if tui.editor.row != 1 {
		t.Fatalf("down arrow should move cursor to row 1, got row=%d", tui.editor.row)
	}
	if tui.editor.col != 2 {
		t.Fatalf("down arrow should preserve col 2, got col=%d", tui.editor.col)
	}
}

func TestNavigateHistory(t *testing.T) {
	tui := newTUI(nil, "s-abc", nil)
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

func TestKittyPageKeysScrollBack(t *testing.T) {
	tui := newTUI(nil, "s-abc", nil)
	tui.rows = 20
	tui.cols = 80
	tui.scrollRows = 5
	for i := 0; i < 20; i++ {
		tui.scroll.appendRaw(styleSystem, "line")
	}
	if quit, err := tui.feed([]byte{27, '[', '5', '7', '3', '5', '4', 'u'}); quit || err != nil {
		t.Fatalf("kitty PageUp feed quit=%v err=%v", quit, err)
	}
	if tui.scroll.scrollOffset == 0 {
		t.Fatal("kitty PageUp did not scroll")
	}
}

func TestPageKeysScrollBack(t *testing.T) {
	tui := newTUI(nil, "s-abc", nil)
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
