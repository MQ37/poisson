package tui

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"poisson/internal/provider"
)

// chunkRecorder records each Write call as a separate chunk in the exact
// order writes happened, safe for concurrent writers — lets a test replay
// the real byte stream a terminal would have received, one write() at a
// time, instead of only inspecting the final settled state.
type chunkRecorder struct {
	mu     sync.Mutex
	chunks []string
}

func (c *chunkRecorder) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.chunks = append(c.chunks, string(p))
	c.mu.Unlock()
	return len(p), nil
}

func (c *chunkRecorder) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.chunks))
	copy(out, c.chunks)
	return out
}

// runRealRenderAndEventLoops spawns goroutines matching lifecycle.go's Run()
// exactly (a 2ms render ticker consuming dirty flags, and an event-drain loop
// applying agent.OutputEvents), returning a stop func. Used so a test
// exercises the real concurrency between "user types" and "turn settles
// asynchronously", not a single-goroutine simulation that can't expose a race
// between them.
func runRealRenderAndEventLoops(e *tuiIntegEnv) (stop func()) {
	stopCh := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		tick := time.NewTicker(2 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-tick.C:
				snap := e.tui.dirty.consume()
				if snap.any() {
					e.tui.paint(snap)
				}
			}
		}
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stopCh:
				return
			case ev := <-e.tui.output:
				e.tui.mu.Lock()
				e.tui.handleEvent(ev)
				e.tui.markAfterEvent(ev)
				e.tui.mu.Unlock()
			}
		}
	}()
	return func() {
		close(stopCh)
		wg.Wait()
	}
}

// TestDuplicateSeparatorAfterCancelThenType is a regression test for a live
// user report: pressing Esc to cancel a running turn (the cancel keybind
// moved from Ctrl+C to Esc), then immediately typing, could leave the input
// separator duplicated on screen. Extensive
// attempts (plain text turns, real concurrent render+event-drain goroutines,
// and cancelling mid-way through a genuinely slow tool call so the turn takes
// real time to settle after Ctrl+C) never reproduced a >1 count here, but the
// completion path this exercises (startTurn's goroutine defer) was hardened
// from markStatus()+markInput() to markFull() as a defensive strengthening —
// several state changes (status flip, queue drain, a "cancelled" scrollback
// line) converge on that one moment, and a full repaint is the one choice
// that structurally cannot leave any of them half-stale for a frame. This
// test pins that scenario down permanently rather than discarding the
// reproduction attempt.
func TestDuplicateSeparatorAfterCancelThenType(t *testing.T) {
	input, _ := json.Marshal(map[string]string{"command": "sleep 0.05"})
	first := []provider.StreamEvent{
		{Type: provider.EventToolUseStart, ToolCall: &provider.ToolCall{ID: "call_1", Name: "bash", Input: input}},
		{Type: provider.EventToolUseStop, ToolCall: &provider.ToolCall{ID: "call_1", Name: "bash", Input: input}},
		{Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 10, OutputTokens: 5}},
	}
	second := provider.FakeTextResponse("done after sleep", nil)

	e := newTUIIntegEnv(t, [][]provider.StreamEvent{first, second})
	rec := &chunkRecorder{}
	e.tui.mu.Lock()
	e.tui.writer = rec
	e.tui.dirty.markFull()
	e.tui.mu.Unlock()
	e.tui.paint(e.tui.dirty.consume())

	stop := runRealRenderAndEventLoops(e)

	e.tui.mu.Lock()
	e.tui.scroll.append(StyledLine{Style: styleUser, Text: "run a slow command"})
	e.tui.startTurn(nil)
	e.tui.mu.Unlock()

	time.Sleep(15 * time.Millisecond) // let the tool call actually start running
	e.tui.feedKey(Key{Kind: KeyEscape})

	for _, r := range "hello there this is a long enough message to wrap around" {
		e.tui.feed([]byte(string(r)))
		time.Sleep(time.Millisecond)
	}
	time.Sleep(150 * time.Millisecond) // let the sleep-based tool actually finish
	stop()

	v := newVterm(e.tui.rows, e.tui.cols)
	max := 0
	for _, chunk := range rec.snapshot() {
		v.apply(chunk)
		if n := countSeparatorRows(v); n > max {
			max = n
		}
	}
	if max > 1 {
		t.Errorf("up to %d separator rows visible simultaneously (ctrl+c during a slow tool, then type), want at most 1", max)
	}
}
