package tui

import (
	"strings"
	"testing"
)

func TestDecoderModifyOtherKeysShiftEnter(t *testing.T) {
	var d Decoder
	keys := d.Push([]byte{27, '[', '2', '7', ';', '2', ';', '1', '3', '~'})
	if len(keys) != 1 || keys[0].Kind != KeyShiftEnter {
		t.Fatalf("keys=%v", keys)
	}
}

func TestConvFocusArrowScrolls(t *testing.T) {
	tui := newTestTUIHelper()
	tui.scrollRows = 8
	tui.scroll.append(StyledLine{Style: styleUser, Text: "first prompt"})
	var filler strings.Builder
	for i := 0; i < 50; i++ {
		filler.WriteString("filler line ")
		filler.WriteString(itoa(i))
		filler.WriteString("\n")
	}
	tui.scroll.appendRaw(styleAssistant, filler.String())
	tui.scroll.append(StyledLine{Style: styleUser, Text: "second prompt"})
	tui.enterConvFocus()
	if tui.focusRegion != focusConv {
		t.Fatal("expected conv focus")
	}
	wrapped, _ := tui.scroll.layoutAll(tui.contentWidth())
	max := len(wrapped) - tui.convScrollRows()
	if max < 1 {
		t.Fatalf("need scrollable content, wrapped=%d max=%d", len(wrapped), max)
	}
	tui.scroll.scrollOffset = max - 1
	if tui.scroll.scrollOffset < 0 {
		tui.scroll.scrollOffset = 0
	}
	before := tui.scroll.scrollOffset

	if !tui.feedConvFocus(Key{Kind: KeyArrowUp}) {
		t.Fatal("feedConvFocus should handle arrow up")
	}
	if tui.scroll.scrollOffset <= before {
		t.Fatalf("arrow up: offset %d want > %d", tui.scroll.scrollOffset, before)
	}
	upOffset := tui.scroll.scrollOffset

	if !tui.feedConvFocus(Key{Kind: KeyArrowDown}) {
		t.Fatal("feedConvFocus should handle arrow down")
	}
	if tui.scroll.scrollOffset >= upOffset {
		t.Fatalf("arrow down: offset %d want < %d", tui.scroll.scrollOffset, upOffset)
	}

	mid := tui.scroll.scrollOffset
	_, err := tui.feedKey(Key{Kind: KeyArrowUp})
	if err != nil {
		t.Fatal(err)
	}
	if tui.scroll.scrollOffset <= mid {
		t.Fatalf("feedKey arrow up: offset %d want > %d", tui.scroll.scrollOffset, mid)
	}
}

func TestTypingWhileAgentRuns(t *testing.T) {
	tui := newTestTUIHelper()
	tui.status.Thinking = true

	_, err := tui.feedKey(Key{Kind: KeyRune, Rune: 'd'})
	if err != nil {
		t.Fatal(err)
	}
	if tui.editor.text() != "d" {
		t.Fatalf("editor=%q want draft while thinking", tui.editor.text())
	}

	_, err = tui.feedKey(Key{Kind: KeyEnter})
	if err != nil {
		t.Fatal(err)
	}
	// Enter while thinking queues the message (sent after the current turn)
	// rather than starting a second turn or being ignored.
	if tui.editor.text() != "" {
		t.Fatalf("enter should clear editor and queue while thinking, editor=%q", tui.editor.text())
	}
	if len(tui.queued) != 1 || tui.queued[0] != "d" {
		t.Fatalf("message not queued: %v", tui.queued)
	}
}

func TestSearchOverlayPaste(t *testing.T) {
	tui := newTestTUIHelper()
	so := newSearchOverlay(func() []ScreenRow { return nil }, nil, nil)
	tui.activeOverlay = so

	_, err := tui.feedKey(Key{Kind: KeyPaste, Text: "foo"})
	if err != nil {
		t.Fatal(err)
	}
	if so.query != "foo" {
		t.Fatalf("query=%q want foo", so.query)
	}
}

func TestDecoderLoneEscape(t *testing.T) {
	var d Decoder
	keys := d.Push([]byte{27})
	if len(keys) != 1 || keys[0].Kind != KeyEscape {
		t.Fatalf("lone esc: keys=%v pending=%q", keys, d.pending)
	}
	if len(d.pending) != 0 {
		t.Fatalf("pending=%q", d.pending)
	}
}

func TestModelPickerLoneEscapeDismisses(t *testing.T) {
	tui := newTestTUIHelper()
	tui.activeOverlay = newPickerOverlay("Models", []pickerItem{
		{id: "a", label: "alpha"},
	}, "", nil)
	quit, err := tui.feed([]byte{27})
	if err != nil || quit {
		t.Fatalf("feed: quit=%v err=%v", quit, err)
	}
	if tui.activeOverlay != nil {
		t.Fatal("lone esc should dismiss model picker")
	}
}

func TestDecoderKittyEscape(t *testing.T) {
	seqs := [][]byte{
		{27, '[', '5', '7', '3', '4', '4', 'u'},
		{27, '[', '5', '7', '3', '4', '4', ';', '1', 'u'},
		{27, '[', '5', '7', '3', '4', '4', ';', '1', ':', '1', 'u'},
		// Kitty keyboard protocol encodes Esc as code 27 (not only PUA 57344).
		{27, '[', '2', '7', ';', '1', 'u'},
		{27, '[', '2', '7', ';', '1', ':', '1', 'u'},
		// Release-only (some remapped keys).
		{27, '[', '2', '7', ';', '1', ':', '3', 'u'},
	}
	for _, seq := range seqs {
		var d Decoder
		keys := d.Push(seq)
		if len(keys) != 1 || keys[0].Kind != KeyEscape {
			t.Fatalf("seq %q: keys=%v", seq, keys)
		}
	}
}

func TestModelPickerKittyEscapeDismisses(t *testing.T) {
	tui := newTestTUIHelper()
	tui.activeOverlay = newPickerOverlay("Models", []pickerItem{
		{id: "a", label: "alpha"},
	}, "", nil)
	// Caps Lock → Esc in Kitty with keyboard protocol enabled.
	quit, err := tui.feed([]byte{27, '[', '2', '7', ';', '1', ':', '1', 'u'})
	if err != nil || quit {
		t.Fatalf("feed: quit=%v err=%v", quit, err)
	}
	if tui.activeOverlay != nil {
		t.Fatal("kitty esc (code 27) should dismiss model picker")
	}
}

func TestApprovalBoxEqualWidth(t *testing.T) {
	o := newApprovalOverlay("rm -rf /tmp/x", "cleanup", "")
	lines := o.renderInputPanel(10, 80)
	if len(lines) != 10 {
		t.Fatalf("expected 10 panel lines, got %d", len(lines))
	}
	for i, ln := range lines {
		if visibleWidth(ln) != 80 {
			t.Fatalf("line %d width %d, want full terminal width 80: %q", i, visibleWidth(ln), stripANSI(ln))
		}
	}
	if !strings.Contains(stripANSI(lines[0]), "Approval required") {
		t.Fatalf("title missing: %q", lines[0])
	}
}

func TestApprovalOverlayShowsLongScript(t *testing.T) {
	var cmd strings.Builder
	for i := 0; i < 40; i++ {
		cmd.WriteString("echo line")
		cmd.WriteByte('\n')
	}
	o := newApprovalOverlay(cmd.String(), "long summer script", "")
	lines := o.renderInputPanel(12, 80)
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "echo line") {
		t.Fatal("expected wrapped command lines in approval box")
	}
	if !strings.Contains(plain, "scroll command") {
		t.Fatal("expected scroll hint for long command")
	}
	if len(lines) != 12 {
		t.Fatalf("expected 12-line input panel, got %d lines", len(lines))
	}
}

func TestApprovalOverlayScrollChangesView(t *testing.T) {
	var cmd strings.Builder
	for i := 0; i < 30; i++ {
		cmd.WriteString("line ")
		cmd.WriteString(itoa(i))
		cmd.WriteByte('\n')
	}
	o := newApprovalOverlay(cmd.String(), "numbered script", "")
	before := o.renderInputPanel(10, 80)
	o.scrollBy(5)
	after := o.renderInputPanel(10, 80)
	if stripANSI(strings.Join(before, "")) == stripANSI(strings.Join(after, "")) {
		t.Fatal("scroll should change visible command lines")
	}
}

func TestHighlightSearchMatchAllOccurrences(t *testing.T) {
	got := stripANSI(highlightSearchMatch("foo bar foo", "foo", "[", "]"))
	if strings.Count(got, "[foo]") != 2 {
		t.Fatalf("expected 2 highlights, got %q", got)
	}
}

func TestHighlightSearchMatchPreservesInlineANSI(t *testing.T) {
	line := bashDangerStyle() + "rm" + reset + " -rf " + bashSafeStyle() + "build" + reset
	got := stripANSI(highlightSearchMatch(line, "rm", "[", "]"))
	if got != "[rm] -rf build" {
		t.Fatalf("highlight shifted with inline ANSI: got %q", got)
	}
	got2 := stripANSI(highlightSearchMatch(line, "build", "[", "]"))
	if got2 != "rm -rf [build]" {
		t.Fatalf("highlight shifted on second span: got %q", got2)
	}
}

func TestTruncateToWidthPreservesANSI(t *testing.T) {
	s := fgRed + "hello" + reset + " world"
	got := truncateToWidth(s, 8)
	if !strings.Contains(got, fgRed) {
		t.Fatalf("expected color preserved: %q", got)
	}
	if visibleWidth(got) > 8 {
		t.Fatalf("too wide: %q (%d)", got, visibleWidth(got))
	}
}

func TestBashInputPreviewMultiline(t *testing.T) {
	cmd := "cat << 'SCRIPT' > /tmp/x.sh\n#!/bin/bash\necho hi\nSCRIPT"
	got := stripANSI(bashInputPreview(cmd))
	if !strings.Contains(got, "cat <<") {
		t.Fatalf("expected first line in preview, got %q", got)
	}
	if !strings.Contains(got, "+3 lines") {
		t.Fatalf("expected line count suffix, got %q", got)
	}
}

func TestOverlayEscapeDismissFallback(t *testing.T) {
	tui := newTestTUIHelper()
	tui.activeOverlay = newPaletteOverlay(nil)
	_, err := tui.feedKey(Key{Kind: KeyEscape})
	if err != nil {
		t.Fatal(err)
	}
	if tui.activeOverlay != nil {
		t.Fatal("escape should dismiss overlay via fallback")
	}
}