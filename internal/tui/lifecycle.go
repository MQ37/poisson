package tui

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/term"
)

// Run starts the alt-screen TUI. It blocks until the user exits.
func (t *TUI) Run() error {
	oldState, err := term.MakeRaw(t.fd)
	if err != nil {
		return fmt.Errorf("enter raw mode: %w", err)
	}
	t.oldState = oldState
	t.recomputeLayout()
	if t.introScrollTop {
		t.introScrollTop = false
		t.scroll.scrollToTop(t.convScrollRows(), t.contentWidth())
	}

	// Wire terminal mode.
	t.writeRaw(altScreenOn + hideCursor + bracketedOn + kittyKbOn + mouseOn)
	t.installResize()

	// Restore terminal on any exit path — including panic — so the user's
	// shell isn't left in raw alt-screen with kitty keyboard enabled.
	defer t.restoreTerminal()

	// Lifecycle channel. render/input goroutines exit when this is closed.
	stop := make(chan struct{})

	// SIGTERM/SIGHUP (kill, ssh disconnect) bypass the defer, so restore the
	// terminal explicitly before exiting — otherwise the shell is left raw.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		select {
		case <-sigCh:
			t.restoreTerminal()
			os.Exit(1)
		case <-stop:
			signal.Stop(sigCh)
		}
	}()
	readCh := make(chan []byte, 8)
	readErr := make(chan error, 1)

	// Initial paint before starting goroutines so wrapWidth is set.
	t.dirty.markFull()
	t.paint(t.dirty.consume())

	// Render goroutine: 30fps redraw on dirty; animates spinners while busy.
	renderDone := make(chan struct{})
	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "poisson: render panic: %v\n", r)
			}
			close(renderDone)
		}()
		tick := time.NewTicker(33 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				t.mu.Lock()
				animate := needsSpinner(t.status.Thinking, t.activeTools)
				if !animate {
					// The /btw panel spins while it streams its answer, even when the
					// main agent is idle.
					if bo, ok := t.activeOverlay.(*btwOverlay); ok {
						_, _, _, processing, _ := bo.snapshot()
						animate = processing
					}
				}
				t.mu.Unlock()
				if animate {
					t.renderFrame++
					t.markSpinnerTick()
				}
				snap := t.dirty.consume()
				if snap.any() {
					t.paint(snap)
				}
			}
		}
	}()

	// Stdin reader: blocking Read runs in its own goroutine so stop can exit promptly.
	go func() {
		buf := make([]byte, 65536)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				readErr <- err
				return
			}
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			select {
			case readCh <- chunk:
			case <-stop:
				return
			}
		}
	}()

	// Input loop.
	go func() {
		defer close(t.done)
		for {
			select {
			case <-stop:
				return
			case <-readErr:
				return
			case data := <-readCh:
				if handled := t.handleMouseInput(data); handled {
					continue
				}
				// Don't scroll scrollback behind modal overlays or approval.
				t.mu.Lock()
				blockBG := t.blocksBackgroundInput()
				t.mu.Unlock()
				if !blockBG {
					if delta, ok := parseMouseWheelScroll(data); ok {
						t.handleScrollDelta(delta)
						continue
					}
				}
				// If an approval prompt is active, route recognized answers to the
				// approval channel instead of feeding them to the editor.
				for _, k := range t.keyDec.Push(data) {
					if t.approving.Load() {
						if k.isCtrlC() {
							t.approvalDenyAndMaybeCancelRun()
							continue
						}
						if k.isNavUp() || k.isNavDown() {
							t.mu.Lock()
							if ao, ok := t.activeOverlay.(*approvalOverlay); ok {
								delta := 1
								if k.isNavUp() {
									delta = -1
								}
								ao.scrollBy(delta)
								t.dirty.markInput()
							}
							t.mu.Unlock()
							continue
						}
						allowed, ok := keyApprovalAnswer(k)
						if ok {
							t.approvalAnswer <- allowed
						} else {
							t.flashApprovalHint()
						}
						continue
					}
					quit, err := t.feedKey(k)
					if err != nil {
						t.appendError(err)
						continue
					}
					if quit {
						t.waitForAgentStop()
						return
					}
				}
			}
		}
	}()

	// Run loop: sole reader of t.output. Drains agent events until input exits.
	for {
		select {
		case <-t.done:
			close(stop)
			<-renderDone
			return nil
		case ev, ok := <-t.output:
			if !ok {
				t.output = nil
				continue
			}
			t.mu.Lock()
			t.handleEvent(ev)
			t.markAfterEvent(ev)
			t.mu.Unlock()
		}
	}
}

// restoreTerminal reverts raw mode and all terminal features. Safe to call
// more than once — the deferred cleanup and the signal handler may both run.
func (t *TUI) restoreTerminal() {
	t.stopped.Store(true)
	t.writeRaw(mouseOff + kittyKbOff + bracketedOff + showCursor + altScreenOff)
	_ = term.Restore(t.fd, t.oldState)
}

// approvalDenyAndMaybeCancelRun rejects the pending approval and cancels an
// in-flight agent turn when one exists.
func (t *TUI) approvalDenyAndMaybeCancelRun() {
	select {
	case t.approvalAnswer <- false:
	default:
		select {
		case <-t.approvalAnswer:
		default:
		}
		select {
		case t.approvalAnswer <- false:
		default:
		}
	}
	t.cancelMu.Lock()
	cancel := t.cancelRun
	t.cancelMu.Unlock()
	if cancel != nil {
		t.cancelActiveRun()
	}
}