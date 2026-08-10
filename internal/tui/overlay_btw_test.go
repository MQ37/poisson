package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/provider"
)

func TestBTWOverlayFillsScrollRegionFullWidth(t *testing.T) {
	o := newBTWOverlay("short question")
	o.appendText("line one\nline two")
	o.finish(nil)
	_, lines := o.render(30, 80)
	// The panel fills the scroll region and every line spans the full width,
	// like the bash-approval overlay.
	if len(lines) != 30 {
		t.Fatalf("panel height = %d, want 30 (fills scroll region)", len(lines))
	}
	for i, ln := range lines {
		if w := visibleWidth(ln); w != 80 {
			t.Fatalf("line %d width = %d, want 80 (full width)", i, w)
		}
	}
}

// TestBTWOverlayUsesSameMarkdownRendererAsConversation confirms /btw's
// answer body goes through layoutRichMarkdown — the same renderer the main
// conversation uses for assistant text — instead of a plain word-wrap. Bold
// text must carry its bold escape, and a fenced code block must render as a
// bordered box (boxTop's ╭ corner), not a bare wrapped dump of the fence
// markers as literal text.
func TestBTWOverlayUsesSameMarkdownRendererAsConversation(t *testing.T) {
	o := newBTWOverlay("q")
	o.appendText("**bold answer**\n\n```go\nfunc f() {}\n```\n")
	o.finish(nil)
	_, lines := o.render(30, 80)
	joined := strings.Join(lines, "\n")

	if !strings.Contains(joined, bold) {
		t.Errorf("expected bold escape in rendered output, got:\n%s", stripANSI(joined))
	}
	if !strings.Contains(stripANSI(joined), "╭") {
		t.Errorf("expected a bordered code block (╭), got:\n%s", stripANSI(joined))
	}
	if strings.Contains(stripANSI(joined), "```") {
		t.Errorf("fence markers should be consumed by the code-block renderer, not shown literally:\n%s", stripANSI(joined))
	}
}

func TestBTWOverlayTitleAndFooter(t *testing.T) {
	o := newBTWOverlay("hi")
	o.finish(nil)
	anchor, lines := o.render(24, 80)
	if anchor != 1 {
		t.Fatalf("anchor = %d, want 1", anchor)
	}
	if !strings.Contains(stripANSI(lines[0]), "btw") {
		t.Fatalf("expected 'btw' title, got %q", stripANSI(lines[0]))
	}
	if !strings.Contains(stripANSI(lines[len(lines)-1]), "Esc") {
		t.Fatalf("expected Esc in footer, got %q", stripANSI(lines[len(lines)-1]))
	}
}

func TestBTWOverlayEscCancel(t *testing.T) {
	o := newBTWOverlay("q")
	cancelled := false
	o.setCancel(func() { cancelled = true })
	handled, done, cancel := o.feedKey(Key{Kind: KeyEscape})
	if !handled || !done || !cancel {
		t.Fatalf("esc while processing: handled=%v done=%v cancel=%v", handled, done, cancel)
	}
	if !cancelled {
		t.Fatal("expected cancel func called")
	}
	o.finish(nil)
	handled, done, cancel = o.feedKey(Key{Kind: KeyEscape})
	if !handled || !done || cancel {
		t.Fatalf("esc when done: handled=%v done=%v cancel=%v", handled, done, cancel)
	}
}

// TestBTWOverlaySetStatusShowsInsteadOfThinking confirms a live tool-call
// status (e.g. "read(main.go)") replaces the bare "thinking…" label while
// no answer text has arrived yet, and is cleared the moment real text
// starts streaming so it never shows a stale tool name once the model has
// moved on.
// TestTUIInteg_BTWRunsReadOnlyToolAndRendersAnswer drives /btw end-to-end
// through the real TUI + Agent: the model calls the allowed "read" tool to
// ground its answer in real file content, and the final answer (not just
// the raw scrollback buffer) renders on screen through the real paint
// pipeline. This is the layer quickanswer_test.go (agent-only) doesn't
// exercise: whether a /btw tool round-trip actually reaches the overlay UI.
func TestTUIInteg_BTWRunsReadOnlyToolAndRendersAnswer(t *testing.T) {
	e := newTUIIntegEnv(t, nil)
	if err := os.WriteFile(filepath.Join(e.dir, "note.txt"), []byte("the secret number is 42"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, second := provider.FakeToolCallResponse("read", map[string]string{"path": "note.txt"}, "the secret number is 42")
	e.prov.SetResponses([][]provider.StreamEvent{first, second})

	e.tui.mu.Lock()
	if err := e.tui.handleSlash("/btw what's the secret number?"); err != nil {
		e.tui.mu.Unlock()
		t.Fatalf("handleSlash: %v", err)
	}
	e.tui.mu.Unlock()

	waitFor(t, func() bool {
		e.tui.mu.Lock()
		defer e.tui.mu.Unlock()
		bo, ok := e.tui.activeOverlay.(*btwOverlay)
		return ok && func() bool {
			_, _, _, processing, _, _ := bo.snapshot()
			return !processing
		}()
	})

	screen := e.render()
	if !strings.Contains(screen, "the secret number is 42") {
		t.Errorf("rendered screen missing the tool-grounded answer, got:\n%s", screen)
	}
}

func TestBTWOverlaySetStatusShowsInsteadOfThinking(t *testing.T) {
	o := newBTWOverlay("q")
	_, lines := o.render(24, 80)
	if !strings.Contains(stripANSI(lines[3]), "thinking…") {
		t.Fatalf("expected default 'thinking…' label, got %q", stripANSI(lines[3]))
	}

	o.setStatus("read(main.go)")
	_, lines = o.render(24, 80)
	if !strings.Contains(stripANSI(lines[3]), "read(main.go)") {
		t.Fatalf("expected status label, got %q", stripANSI(lines[3]))
	}

	o.appendText("here's the answer")
	_, lines = o.render(24, 80)
	joined := stripANSI(strings.Join(lines, "\n"))
	if strings.Contains(joined, "read(main.go)") {
		t.Fatalf("stale tool status should be cleared once text arrives, got:\n%s", joined)
	}
}

func TestBTWOverlayScroll(t *testing.T) {
	o := newBTWOverlay("q")
	var lines []string
	for i := 0; i < 40; i++ {
		lines = append(lines, "answer line "+strings.Repeat("x", 20))
	}
	o.appendText(strings.Join(lines, "\n"))
	o.finish(nil)
	// Render once so the overlay knows its scroll bounds.
	o.render(12, 80)
	o.feedKey(keyArrowDown())
	if _, _, _, _, scroll, _ := o.snapshot(); scroll == 0 {
		t.Fatal("expected scroll down to increase offset")
	}
	o.feedKey(keyArrowUp())
	if _, _, _, _, scroll, _ := o.snapshot(); scroll != 0 {
		t.Fatalf("scroll = %d after up, want 0", scroll)
	}
}

// TestBTWOverlayScrollBy is scrollBy's own unit coverage — the mouse-wheel
// counterpart to TestBTWOverlayScroll's arrow-key coverage. Regression for
// the bug where a wheel tick over the /btw answer panel did nothing at all
// (handleOneMouseEvent had no case for *btwOverlay, so it fell into the
// generic "blocksBackgroundInput, no recognized overlay" no-op branch).
func TestBTWOverlayScrollBy(t *testing.T) {
	o := newBTWOverlay("q")
	o.scrollBy(5)
	if _, _, _, _, scroll, _ := o.snapshot(); scroll != 5 {
		t.Fatalf("scroll = %d after scrollBy(5), want 5", scroll)
	}
	o.scrollBy(-2)
	if _, _, _, _, scroll, _ := o.snapshot(); scroll != 3 {
		t.Fatalf("scroll = %d after scrollBy(-2), want 3", scroll)
	}
	o.scrollBy(-100)
	if _, _, _, _, scroll, _ := o.snapshot(); scroll != 0 {
		t.Fatalf("scroll = %d after large negative scrollBy, want floored at 0", scroll)
	}
}
