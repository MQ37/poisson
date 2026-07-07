package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"poisson/internal/provider"
)

// waitUntilLocked polls cond (which acquires t.mu itself) until true or timeout.
func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

// TestQueue_EnqueueWhileRunning: submitting while a turn is in flight queues the
// message and clears the editor rather than starting a second turn.
func TestQueue_EnqueueWhileRunning(t *testing.T) {
	e := newTUIIntegEnv(t, nil)
	e.tui.mu.Lock()
	e.tui.status.Thinking = true
	e.tui.enqueueLocked("second message")
	e.tui.enqueueLocked("third message")
	n := len(e.tui.queued)
	editorText := e.tui.editor.text()
	e.tui.mu.Unlock()

	if n != 2 {
		t.Errorf("queued = %d, want 2", n)
	}
	if editorText != "" {
		t.Errorf("editor not cleared after enqueue: %q", editorText)
	}
	// No extra turn was started (fake provider never called).
	if c := e.prov.CallCount(); c != 0 {
		t.Errorf("provider called %d times while queueing, want 0", c)
	}
}

// TestQueue_SlashNotQueued: slash commands can't be queued while busy.
func TestQueue_SlashNotQueued(t *testing.T) {
	e := newTUIIntegEnv(t, nil)
	e.tui.mu.Lock()
	e.tui.status.Thinking = true
	e.tui.enqueueLocked("/model")
	n := len(e.tui.queued)
	hint := e.tui.status.Hint
	e.tui.mu.Unlock()

	if n != 0 {
		t.Errorf("slash command queued (%d); should be rejected", n)
	}
	if !strings.Contains(hint, "queue") {
		t.Errorf("no hint about slash rejection: %q", hint)
	}
}

// TestQueue_DrainSendsCombinedTurn: draining sends every queued message as a
// single follow-up turn, shows each in the conversation, and clears the queue.
func TestQueue_DrainSendsCombinedTurn(t *testing.T) {
	e := newTUIIntegEnv(t, [][]provider.StreamEvent{
		{{Type: provider.EventTextDelta, Text: "done"}, {Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 5, OutputTokens: 2}}},
	})
	// Drain the output channel in the background so the agent goroutine never
	// blocks (mirrors the real run loop).
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			case ev := <-e.tui.output:
				e.tui.mu.Lock()
				e.tui.handleEvent(ev)
				e.tui.markAfterEvent(ev)
				e.tui.mu.Unlock()
			}
		}
	}()
	defer close(stop)

	e.tui.mu.Lock()
	e.tui.queued = []string{"first task", "second task"}
	e.tui.drainQueueLocked()
	e.tui.mu.Unlock()

	waitUntil(t, func() bool {
		e.tui.mu.Lock()
		defer e.tui.mu.Unlock()
		return !e.tui.status.Thinking
	})

	// Both queued messages appear in the conversation.
	st := e.scrollText()
	if !strings.Contains(st, "first task") || !strings.Contains(st, "second task") {
		t.Errorf("queued messages missing from scrollback: %q", st)
	}
	// A single combined turn was sent (one provider call).
	if c := e.prov.CallCount(); c != 1 {
		t.Errorf("provider call count = %d, want 1 (single combined turn)", c)
	}
	// The combined prompt contains both messages.
	req := e.prov.LastRequest()
	if req == nil {
		t.Fatal("no request captured")
	}
	var sent string
	for _, m := range req.Messages {
		if m.Role == "user" {
			for _, c := range m.Content {
				sent += c.Text
			}
		}
	}
	if !strings.Contains(sent, "first task") || !strings.Contains(sent, "second task") {
		t.Errorf("combined prompt missing queued content: %q", sent)
	}
	// Queue is drained.
	e.tui.mu.Lock()
	left := len(e.tui.queued)
	e.tui.mu.Unlock()
	if left != 0 {
		t.Errorf("queue not cleared after drain: %d left", left)
	}
}

// TestQueue_CancelClearsQueue: Ctrl+C abandons queued messages.
func TestQueue_CancelClearsQueue(t *testing.T) {
	e := newTUIIntegEnv(t, nil)
	e.tui.mu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	e.tui.cancelMu.Lock()
	e.tui.cancelCtx = ctx
	e.tui.cancelRun = cancel
	e.tui.cancelMu.Unlock()
	e.tui.status.Thinking = true
	e.tui.queued = []string{"a", "b"}
	e.tui.cancelActiveRunLocked()
	n := len(e.tui.queued)
	e.tui.mu.Unlock()
	cancel()

	if n != 0 {
		t.Errorf("queue not cleared on cancel: %d", n)
	}
}

// TestQueue_PreviewRendering: queued messages show as dimmed preview rows above
// the input, with an overflow summary beyond the cap.
func TestQueue_PreviewRendering(t *testing.T) {
	e := newTUIIntegEnv(t, nil)
	e.tui.mu.Lock()
	e.tui.status.Thinking = true
	e.tui.queued = []string{"alpha task", "beta task", "gamma task", "delta task"}
	e.tui.mu.Unlock()

	if got := e.tui.queuedPreviewRows(); got != maxQueuedPreview {
		t.Errorf("queuedPreviewRows = %d, want %d", got, maxQueuedPreview)
	}
	out := e.render()
	if !strings.Contains(out, "alpha task") || !strings.Contains(out, "beta task") {
		t.Errorf("preview missing first queued messages:\n%s", out)
	}
	if !strings.Contains(out, "more queued") {
		t.Errorf("preview missing overflow summary:\n%s", out)
	}
	if !strings.Contains(out, "queue message") {
		t.Errorf("hint missing queue affordance:\n%s", out)
	}
}

// TestQueue_BTWDispatchesWhileBusy: /btw launches immediately during a live
// turn (side question), rather than being blocked like other slash commands.
func TestQueue_BTWDispatchesWhileBusy(t *testing.T) {
	e := newTUIIntegEnv(t, nil)
	e.tui.mu.Lock()
	e.tui.status.Thinking = true
	e.tui.enqueueLocked("/btw what is 2+2")
	queued := len(e.tui.queued)
	_, isBTW := e.tui.activeOverlay.(*btwOverlay)
	e.tui.mu.Unlock()

	if queued != 0 {
		t.Errorf("/btw should not be queued, got %d queued", queued)
	}
	if !isBTW {
		t.Error("/btw should open the btw overlay immediately while busy")
	}
}
