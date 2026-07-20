package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"poisson/internal/agent"
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

// TestQueue_DrainMatchesResume is a regression test: a drained queue of
// several messages must render identically live and on resume — one bubble,
// not one per originally-queued message (they're already sent and stored as
// a single combined turn; hydrate.go always reconstructs one stored user row
// as one bubble, so live must match that instead of showing N).
func TestQueue_DrainMatchesResume(t *testing.T) {
	e := newTUIIntegEnv(t, [][]provider.StreamEvent{
		{{Type: provider.EventTextDelta, Text: "done"}, {Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 5, OutputTokens: 2}}},
	})
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
	liveUserBlocks := 0
	for _, b := range e.tui.scroll.blocks {
		if b.kind == blockUser {
			liveUserBlocks++
		}
	}
	e.tui.mu.Unlock()

	waitUntil(t, func() bool {
		e.tui.mu.Lock()
		defer e.tui.mu.Unlock()
		return !e.tui.status.Thinking
	})

	e.tui.mu.Lock()
	e.tui.resetSessionViewLocked()
	resumedUserBlocks := 0
	for _, b := range e.tui.scroll.blocks {
		if b.kind == blockUser {
			resumedUserBlocks++
		}
	}
	e.tui.mu.Unlock()

	if liveUserBlocks != resumedUserBlocks {
		t.Errorf("live shows %d user bubble(s), resume shows %d — must match", liveUserBlocks, resumedUserBlocks)
	}
	if liveUserBlocks != 1 {
		t.Errorf("drained queue rendered as %d bubbles, want exactly 1 (one combined turn)", liveUserBlocks)
	}
	st := e.scrollText()
	if !strings.Contains(st, "first task") || !strings.Contains(st, "second task") {
		t.Errorf("queued messages missing from resumed scrollback: %q", st)
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

// TestQueue_EnqueueWhileManuallyCompacting is the reported bug: typing and
// submitting a message during a manual /compact (no turn running — status.
// Thinking is false — but t.compacting is true) used to fall through to
// submit()'s own hard rejection ("cannot submit while compacting"),
// discarding the message, because the queue-vs-submit routing only checked
// running() (Thinking), not compacting. It must be queued instead, same as
// during a live turn.
func TestQueue_EnqueueWhileManuallyCompacting(t *testing.T) {
	e := newTUIIntegEnv(t, nil)
	e.tui.mu.Lock()
	e.tui.compacting.Store(true)
	e.tui.mu.Unlock()

	for _, r := range "hello" {
		e.tui.feedKey(Key{Kind: KeyRune, Rune: r})
	}
	e.tui.feedKey(Key{Kind: KeyEnter})

	e.tui.mu.Lock()
	n := len(e.tui.queued)
	e.tui.mu.Unlock()
	if n != 1 {
		t.Fatalf("queued = %d, want 1 (message should be queued, not rejected)", n)
	}
	if st := e.scrollText(); strings.Contains(st, "cannot submit while compacting") {
		t.Errorf("message was rejected instead of queued: %q", st)
	}
	// No turn was started (would mean it went through submit() instead).
	if c := e.prov.CallCount(); c != 0 {
		t.Errorf("provider called %d times, want 0 (queued, not submitted)", c)
	}
}

// TestFinishManualCompactDrainsQueuedMessage verifies a message queued
// during a manual /compact is sent (as a fresh turn) once compaction
// finishes, whether it succeeded or failed — the queue must never be left
// stranded just because there was nothing to compact or compaction errored.
func TestFinishManualCompactDrainsQueuedMessage(t *testing.T) {
	for _, compactErr := range []error{nil, agent.ErrNothingToCompact} {
		e := newTUIIntegEnv(t, [][]provider.StreamEvent{
			{{Type: provider.EventTextDelta, Text: "done"}, {Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 5, OutputTokens: 2}}},
		})
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

		e.tui.mu.Lock()
		e.tui.compacting.Store(true)
		e.tui.queued = []string{"queued during compact"}
		e.tui.finishManualCompactLocked(compactErr)
		e.tui.mu.Unlock()

		waitUntil(t, func() bool {
			e.tui.mu.Lock()
			defer e.tui.mu.Unlock()
			return !e.tui.status.Thinking
		})
		close(stop)

		if e.tui.compacting.Load() {
			t.Errorf("err=%v: compacting should be cleared", compactErr)
		}
		e.tui.mu.Lock()
		left := len(e.tui.queued)
		e.tui.mu.Unlock()
		if left != 0 {
			t.Errorf("err=%v: queue not drained after compaction finished, %d left", compactErr, left)
		}
		if c := e.prov.CallCount(); c != 1 {
			t.Errorf("err=%v: expected the queued message to start exactly one turn, got %d calls", compactErr, c)
		}
		if st := e.scrollText(); !strings.Contains(st, "queued during compact") {
			t.Errorf("err=%v: queued message missing from scrollback: %q", compactErr, st)
		}
	}
}

// TestQueue_TakeQueuedForInjectionDrainsAndDisplays verifies the agent's
// pendingInputFn hook (wired via a.SetPendingInputFn in main.go) drains and
// displays queued messages exactly like drainQueueLocked, without starting a
// new top-level turn — the running turn's own loop is what continues.
func TestQueue_TakeQueuedForInjectionDrainsAndDisplays(t *testing.T) {
	e := newTUIIntegEnv(t, nil)
	e.tui.mu.Lock()
	e.tui.status.Thinking = true
	e.tui.queued = []string{"first task", "second task"}
	e.tui.mu.Unlock()

	segs, ok := e.tui.TakeQueuedForInjection()
	if !ok {
		t.Fatal("expected ok=true with messages queued")
	}
	var text string
	for _, s := range segs {
		text += s.Text
	}
	if !strings.Contains(text, "first task") || !strings.Contains(text, "second task") {
		t.Errorf("segments missing queued content: %q", text)
	}

	e.tui.mu.Lock()
	left := len(e.tui.queued)
	e.tui.mu.Unlock()
	if left != 0 {
		t.Errorf("queue not drained: %d left", left)
	}

	st := e.scrollText()
	if !strings.Contains(st, "first task") || !strings.Contains(st, "second task") {
		t.Errorf("queued messages missing from scrollback: %q", st)
	}
	// No turn was started here — TakeQueuedForInjection only feeds an
	// already-running loop; it never calls startTurn itself.
	if c := e.prov.CallCount(); c != 0 {
		t.Errorf("provider called %d times, want 0 (no new turn started)", c)
	}
}

// TestQueue_TakeQueuedForInjectionEmptyQueue verifies ok=false with nothing
// queued, so the turn loop's caller knows not to inject anything.
func TestQueue_TakeQueuedForInjectionEmptyQueue(t *testing.T) {
	e := newTUIIntegEnv(t, nil)
	segs, ok := e.tui.TakeQueuedForInjection()
	if ok || segs != nil {
		t.Errorf("expected (nil, false) for an empty queue, got (%v, %v)", segs, ok)
	}
}

// TestQueue_TakeQueuedForInjectionBlockedDuringCompaction verifies a queued
// message is left alone (not drained) while compaction is running, matching
// drainQueueLocked's own guard — can't submit mid-compaction.
func TestQueue_TakeQueuedForInjectionBlockedDuringCompaction(t *testing.T) {
	e := newTUIIntegEnv(t, nil)
	e.tui.mu.Lock()
	e.tui.queued = []string{"a"}
	e.tui.mu.Unlock()
	e.tui.compacting.Store(true)

	segs, ok := e.tui.TakeQueuedForInjection()
	if ok || segs != nil {
		t.Errorf("expected (nil, false) during compaction, got (%v, %v)", segs, ok)
	}
	e.tui.mu.Lock()
	left := len(e.tui.queued)
	e.tui.mu.Unlock()
	if left != 1 {
		t.Errorf("message should remain queued during compaction, got %d left", left)
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

// TestQueue_NameDispatchesWhileBusy: /name only writes the session's title
// (store metadata, not turn/message state) and must run immediately during a
// live turn instead of being blocked like other slash commands — the same
// reasoning that already exempts /btw. Regression test: /name used to fall
// through to the generic "can't queue a / command while busy" rejection,
// silently discarding the title change instead of applying it.
func TestQueue_NameDispatchesWhileBusy(t *testing.T) {
	e := newTUIIntegEnv(t, nil)
	e.tui.mu.Lock()
	e.tui.status.Thinking = true
	e.tui.enqueueLocked("/name my great session")
	queued := len(e.tui.queued)
	hint := e.tui.status.Hint
	e.tui.mu.Unlock()

	if queued != 0 {
		t.Errorf("/name should not be queued, got %d queued", queued)
	}
	if strings.Contains(hint, "can't queue") {
		t.Errorf("/name should not hit the busy-rejection hint, got %q", hint)
	}
	sess, err := e.store.GetSession(e.sid)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.Title == nil || *sess.Title != "my great session" {
		t.Errorf("session title = %v, want %q — /name should apply immediately while busy", sess.Title, "my great session")
	}
}
