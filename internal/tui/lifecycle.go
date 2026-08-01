package tui

import (
	"context"
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

	// SIGINT/SIGTERM/SIGHUP (Ctrl+C sent externally, kill, ssh disconnect)
	// bypass the defer, so restore the terminal explicitly before exiting —
	// otherwise the shell is left raw. Also cancel any in-flight agent turn
	// and give it a bounded moment to actually stop, same as the normal quit
	// path (waitForAgentStop), instead of just os.Exit'ing out from under a
	// running tool/subprocess/LLM stream and orphaning it.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		select {
		case <-sigCh:
			t.waitForAgentStop()
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
				var animate bool
				t.withLock(func() {
					animate = needsSpinner(t.status.Thinking, t.activeTools, t.compacting.Load())
					if !animate {
						// The /btw panel spins while it streams its answer, even when the
						// main agent is idle.
						if bo, ok := t.activeOverlay.(*btwOverlay); ok {
							_, _, _, processing, _, _ := bo.snapshot()
							animate = processing
						}
					}
				})
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

	// Background Anthropic/OpenAI usage-limit refresh: a no-op for whichever
	// provider isn't active (RefreshAnthropicUsageLimits/RefreshOpenAIUsage
	// Limits check internally). Ticks at the same cadence as each provider's
	// own 5-minute cache TTL (anthropic_usage.go/openai_usage.go) — a poll
	// would just return the cache anyway, so there's no reason to poll more
	// often than the value can actually change. triggerUsageRefreshLocked
	// (provider/session switches) resets this ticker's schedule via
	// usageTickerReset so an explicit refresh and the next scheduled one
	// don't land moments apart.
	usageCtx, cancelUsage := context.WithCancel(context.Background())
	go func() {
		defer cancelUsage()
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "poisson: usage-refresh panic: %v\n", r)
			}
		}()
		t.refreshProviderUsageLimitsForce(usageCtx)
		tick := time.NewTicker(5 * time.Minute)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				t.refreshProviderUsageLimits(usageCtx)
			case <-t.usageTickerReset:
				tick.Reset(5 * time.Minute)
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

	// Input loop. This is the largest, most panic-prone surface in the TUI
	// (key dispatch, overlays, slash commands) — recover so a bug here closes
	// t.done and lets the run loop's normal exit path restore the terminal,
	// instead of an unrecovered panic skipping Run()'s defer entirely and
	// leaving the shell in raw/alt-screen mode.
	go func() {
		defer close(t.done)
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "poisson: input loop panic: %v\n", r)
			}
		}()
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
				var blockBG bool
				t.withLock(func() { blockBG = t.blocksBackgroundInput() })
				// Approval keeps the conversation visible above the prompt, so allow
				// wheel-scrolling it too (other full-screen overlays stay blocked).
				if !blockBG || t.approving.Load() {
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
							// Panic button: send the denial right now, discarding any
							// reason typed so far, even if a reason prompt is up.
							t.approvalDenyAndMaybeCancelRun("")
							continue
						}
						if t.feedDenyReasonKey(k) {
							continue
						}
						// Let the user review the conversation while deciding: Tab
						// toggles conversation focus and scroll keys move the
						// conversation, all with the approval still pending.
						var convFocus, routes bool
						t.withLock(func() {
							convFocus = t.focusRegion == focusConv
							routes = approvalRoutesToHandler(k, convFocus, t.scrollRows)
						})
						if routes {
							if quit, err := t.feedKey(k); err != nil {
								t.appendError(err)
							} else if quit {
								t.waitForAgentStop()
								return
							}
							continue
						}
						// Input focus: arrows scroll the (possibly long) command panel.
						if !convFocus && (k.isNavUp() || k.isNavDown()) {
							t.withLock(func() {
								if ao, ok := t.activeOverlay.(*approvalOverlay); ok {
									delta := 1
									if k.isNavUp() {
										delta = -1
									}
									ao.scrollBy(delta)
									t.dirty.markInput()
								}
							})
							continue
						}
						allowed, ok := keyApprovalAnswer(k)
						switch {
						case !ok:
							t.flashApprovalHint()
						case allowed:
							t.approvalAnswer <- approvalReply{Allowed: true}
						default:
							// Denying commits to the deny, but doesn't send it yet: show an
							// optional reason prompt first. feedDenyReasonKey (above) takes
							// over from here until Enter/Esc finalizes it.
							t.withLock(func() {
								if ao, ok := t.activeOverlay.(*approvalOverlay); ok {
									ao.beginDenyReason()
									t.dirty.markInput()
								}
							})
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
			t.withLock(func() {
				t.handleEvent(ev)
				t.markAfterEvent(ev)
			})
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

// approvalDenyAndMaybeCancelRun rejects the pending approval. With no reason
// (Ctrl+C's panic-deny, or an empty reason confirmed from the prompt) it also
// cancels the in-flight agent turn, same as before this feature existed —
// there's nothing to tell the model, so there's nothing for it to usefully
// continue with. With a non-empty reason, the turn is left running: the
// reason is forwarded to the model as the tool's result and the agent loop
// continues normally, letting the model adjust its plan instead of being cut
// off mid-turn.
func (t *TUI) approvalDenyAndMaybeCancelRun(reason string) {
	reply := approvalReply{Allowed: false, Reason: reason}
	select {
	case t.approvalAnswer <- reply:
	default:
		select {
		case <-t.approvalAnswer:
		default:
		}
		select {
		case t.approvalAnswer <- reply:
		default:
		}
	}
	if reason != "" {
		return
	}
	t.cancelMu.Lock()
	cancel := t.cancelRun
	t.cancelMu.Unlock()
	if cancel != nil {
		t.cancelActiveRun()
	}
}

// feedDenyReasonKey routes a keystroke to the reason text field once the user
// has committed to denying (d/n/Esc) but hasn't confirmed yet. Enter/Escape
// finalize the deny with whatever's typed (possibly empty); Ctrl+C is handled
// by the caller before this is reached and always sends an empty reason.
// Returns false when there's no pending reason prompt, so the caller falls
// through to its normal approval-key handling.
func (t *TUI) feedDenyReasonKey(k Key) bool {
	// approvalDenyAndMaybeCancelRun (below) can call t.cancelActiveRun,
	// which itself takes t.mu — so the Enter/Escape finalize path must run
	// AFTER this function's own lock releases, same reasoning as
	// handleOneMouseEvent's deferred handleScrollDelta call.
	var handled, doFinalize bool
	var finalizeReason string
	t.withLock(func() {
		ao, ok := t.activeOverlay.(*approvalOverlay)
		if !ok || !ao.denying {
			return
		}
		handled = true
		if k.Kind == KeyEnter || k.Kind == KeyEscape {
			finalizeReason = ao.reasonText()
			doFinalize = true
			return
		}
		// Same editor the main input box uses, so every key (word-wise
		// Alt+Backspace/Alt+Arrow, Ctrl+W, Home/End, paste, ...) behaves
		// identically here. The quit signal applyKey returns for Ctrl+D on an
		// empty buffer means nothing in a reason prompt — ignored, not acted on.
		if ao.reasonEditor.wrapWidth < 1 && t.cols > 0 {
			ao.reasonEditor.wrapWidth = inputWrapWidth(t.cols)
		}
		ao.reasonEditor.applyKey(k)
		t.dirty.markInput()
	})
	if doFinalize {
		t.approvalDenyAndMaybeCancelRun(finalizeReason)
	}
	return handled
}
