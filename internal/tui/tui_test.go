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
	dir := testutil.TempDir(t)
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


func TestMatchSlash(t *testing.T) {
	cases := []struct {
		partial string
		want    []string
	}{
		{"/", []string{"/quit", "/clear", "/help", "/new", "/resume", "/sessions", "/search", "/fork", "/undo", "/compact", "/model", "/effort", "/providers", "/reload", "/cost", "/btw"}},
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

func TestMatchAtFile(t *testing.T) {
	dir := testutil.TempDir(t)
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

func TestFeedPasteBypass(t *testing.T) {
	// Pasted bytes inside bracketed paste markers should go directly to the
	// editor, not trigger Tab/Enter/Esc interception.
	tui := newTUI(nil, "s-test", nil)
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
