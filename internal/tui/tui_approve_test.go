package tui

import (
	"io"
	"testing"
	"time"
)

func newTestTUIHelper() *TUI {
	tui := newTUI(nil, "s-test", nil)
	tui.rows = 24
	tui.cols = 80
	tui.scrollRows = 20
	tui.writer = io.Discard
	return tui
}

func TestApproveLifecycle(t *testing.T) {
	tui := newTestTUIHelper()
	result := make(chan bool, 1)
	go func() {
		result <- tui.Approve("rm -rf x", "danger")
	}()

	deadline := time.Now().Add(500 * time.Millisecond)
	for !tui.approving.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !tui.approving.Load() {
		t.Fatal("Approve never entered approving state")
	}

	tui.approvalAnswer <- true

	var got bool
	select {
	case got = <-result:
	case <-time.After(time.Second):
		t.Fatal("Approve timed out")
	}
	if !got {
		t.Fatal("expected allow")
	}
	tui.mu.Lock()
	blocks := tui.scroll.blockCount()
	tui.mu.Unlock()
	if blocks != 1 {
		t.Fatalf("expected approval result in scrollback, blocks=%d", blocks)
	}
}

func TestScrollHandledBeforeApproval(t *testing.T) {
	tui := newTestTUIHelper()
	tui.mu.Lock()
	for i := 0; i < 30; i++ {
		tui.scroll.appendRaw(styleSystem, "line")
	}
	tui.mu.Unlock()

	raw := []byte("\x1b[5~")
	// Mirrors input-loop order: scroll gestures return early; approval routing
	// never runs on the same chunk.
	delta, scrolled := parseScrollInputRaw(raw, tui.scrollRows)
	if !scrolled || delta <= 0 {
		t.Fatalf("page up: delta=%d ok=%v", delta, scrolled)
	}
	tui.handleScrollDelta(delta)
	if tui.scroll.scrollOffset == 0 {
		t.Fatal("expected scroll offset > 0")
	}
}

func TestEditorDeleteKeyCSI(t *testing.T) {
	e := newEditor()
	e.setText("ab")
	e.col = 1
	consumed, submitted := e.handleEscape([]byte{27, '[', '3', '~'})
	if submitted || consumed != 4 {
		t.Fatalf("delete CSI: consumed=%d submitted=%v", consumed, submitted)
	}
	if e.text() != "a" {
		t.Fatalf("after delete: %q", e.text())
	}
}

func TestApproveWhileAgentRunning(t *testing.T) {
	tui := newTestTUIHelper()
	tui.status.Thinking = true
	result := make(chan bool, 1)
	go func() {
		result <- tui.Approve("rm -rf x", "danger")
	}()
	deadline := time.Now().Add(500 * time.Millisecond)
	for !tui.approving.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !tui.approving.Load() {
		t.Fatal("approving not set while agent running")
	}
	allowed, ok := approvalKeyAllowed([]byte{'a'})
	if !ok || !allowed {
		t.Fatal("expected allow key")
	}
	select {
	case tui.approvalAnswer <- allowed:
	default:
		t.Fatal("approval answer channel full")
	}
	select {
	case got := <-result:
		if !got {
			t.Fatal("expected allow")
		}
	case <-time.After(time.Second):
		t.Fatal("Approve timed out while agent running")
	}
}

func TestFeedArrowRightMovesCursor(t *testing.T) {
	tui := newTestTUIHelper()
	tui.editor.setText("abc")
	tui.editor.col = 1
	quit, err := tui.feed([]byte{27, '[', 'C'})
	if err != nil || quit {
		t.Fatalf("feed: quit=%v err=%v", quit, err)
	}
	if tui.editor.col != 2 {
		t.Fatalf("col=%d want 2", tui.editor.col)
	}
}

func TestFeedPlainArrowNotScrollback(t *testing.T) {
	tui := newTestTUIHelper()
	tui.scroll.appendRaw(styleSystem, "history")
	tui.scroll.scrollToBottom()
	before := tui.scroll.scrollOffset
	_, _ = tui.feed([]byte{27, '[', 'A'})
	if tui.scroll.scrollOffset != before {
		t.Fatalf("plain up scrolled offset %d -> %d", before, tui.scroll.scrollOffset)
	}
}

func TestSplitPrefixUnicode(t *testing.T) {
	line := "prefix @café"
	col := len([]rune(line))
	_, tok := splitPrefix(line, col)
	if tok != "@café" {
		t.Fatalf("token = %q, want %q", tok, "@café")
	}
}