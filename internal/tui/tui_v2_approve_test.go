package tui

import (
	"io"
	"testing"
	"time"
)

func newTestTUIv2() *tuiV2 {
	tui := newTUIv2(nil, "s-test", nil)
	tui.rows = 24
	tui.cols = 80
	tui.scrollRows = 20
	tui.writer = io.Discard
	return tui
}

func TestV2ApproveLifecycle(t *testing.T) {
	tui := newTestTUIv2()
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
	tui := newTestTUIv2()
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

func TestSplitPrefixUnicode(t *testing.T) {
	line := "prefix @café"
	col := len([]rune(line))
	_, tok := splitPrefix(line, col)
	if tok != "@café" {
		t.Fatalf("token = %q, want %q", tok, "@café")
	}
}