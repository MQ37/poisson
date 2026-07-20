package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"poisson/internal/agent"
)

func (t *TUI) handleSlash(cmd string) error {
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return nil
	}
	h := tuiCmdHost{t}
	switch parts[0] {
	case "/quit", "/q":
		return errQuitSentinel
	case "/clear":
		if t.sessionBusyLocked() {
			t.scroll.appendRaw(styleSystem, "cannot clear while agent is running or compacting")
			t.markScrollDirty()
			return nil
		}
		t.clearScrollbackKeepIntroLocked()
		t.markFullDirty()
		t.scroll.appendRaw(styleSystem, "display cleared (session history unchanged)")
		t.markScrollDirty()
		return nil
	case "/help", "/h", "/?":
		t.scroll.appendRaw(styleSystem, renderHelp())
		t.markScrollDirty()
		return nil
	case "/name":
		return cmdName(h, parts[1:])
	case "/new":
		if t.sessionBusyLocked() {
			t.scroll.appendRaw(styleSystem, "cannot create session while agent is running or compacting")
			t.markScrollDirty()
			return nil
		}
		return cmdNew(h)
	case "/resume", "/r":
		if t.compacting.Load() {
			t.scroll.appendRaw(styleSystem, "cannot switch session while compacting")
			t.markScrollDirty()
			return nil
		}
		if len(parts) == 1 {
			t.openSessionPicker()
			return nil
		}
		return cmdResume(h, parts[1:])
	case "/sessions":
		if t.compacting.Load() {
			t.scroll.appendRaw(styleSystem, "cannot switch session while compacting")
			t.markScrollDirty()
			return nil
		}
		t.openSessionPicker()
		return nil
	case "/search":
		if len(parts) == 1 {
			t.openSearchLocked()
			return nil
		}
		return cmdSearch(h, parts[1:])
	case "/compact":
		if t.sessionBusyLocked() {
			t.scroll.appendRaw(styleSystem, "cannot compact while agent is running or compacting")
			t.markScrollDirty()
			return nil
		}
		t.compacting.Store(true)
		t.scroll.appendRaw(styleSystem, "  compacting context...")
		t.markScrollDirty()
		go func() {
			err := t.agent.Compact()
			t.mu.Lock()
			defer t.mu.Unlock()
			t.finishManualCompactLocked(err)
		}()
		return nil
	case "/model":
		if t.sessionBusyLocked() {
			t.scroll.appendRaw(styleSystem, "cannot change model while agent is running or compacting")
			t.markScrollDirty()
			return nil
		}
		if len(parts) == 1 {
			t.openModelPicker()
			return nil
		}
		return cmdModel(h, parts[1:])
	case "/providers":
		if t.sessionBusyLocked() {
			t.scroll.appendRaw(styleSystem, "cannot change provider while agent is running or compacting")
			t.markScrollDirty()
			return nil
		}
		t.openProviderPicker()
		return nil
	case "/effort":
		if t.sessionBusyLocked() {
			if len(parts) > 1 {
				t.scroll.appendRaw(styleSystem, "cannot change effort while agent is running or compacting")
				t.markScrollDirty()
			} else {
				t.setEphemeralHintLocked("cannot change effort while agent is running or compacting", 3*time.Second)
			}
			return nil
		}
		if len(parts) == 1 {
			t.openEffortPicker()
			return nil
		}
		return cmdEffort(h, parts[1:])
	case "/reload":
		if t.sessionBusyLocked() {
			t.scroll.appendRaw(styleSystem, "cannot reload while agent is running or compacting")
			t.markScrollDirty()
			return nil
		}
		return cmdReload(h)
	case "/cost":
		cmdCost(h)
		return nil
	case "/openai-reset-usage":
		if t.sessionBusyLocked() {
			t.scroll.appendRaw(styleSystem, "cannot reset usage while agent is running or compacting")
			t.markScrollDirty()
			return nil
		}
		t.scroll.appendRaw(styleSystem, "  resetting Codex usage window...")
		t.markScrollDirty()
		go func() {
			result, err := t.agent.ResetOpenAIUsage(context.Background())
			t.mu.Lock()
			defer t.mu.Unlock()
			if err != nil {
				t.scroll.appendRaw(styleError, "reset usage failed: "+err.Error())
			} else {
				t.scroll.appendRaw(styleSystem, fmt.Sprintf("usage window reset (%d window(s) reset, %d reset credit(s) remaining)", result.WindowsReset, result.CreditsRemaining))
				t.syncHeaderFromAgentLocked()
				t.dirty.markStatus()
			}
			t.markScrollDirty()
		}()
		return nil
	case "/status":
		cmdStatus(h)
		return nil
	case "/btw":
		question := strings.TrimSpace(strings.TrimPrefix(cmd, parts[0]))
		if question == "" {
			t.scroll.appendRaw(styleSystem, "usage: /btw <question>")
			t.markScrollDirty()
			return nil
		}
		t.openBTW(question)
		return nil
	default:
		t.scroll.appendRaw(styleSystem, "unknown command: "+parts[0]+" (type /help)")
		t.markScrollDirty()
		return nil
	}
}

// finishManualCompactLocked is the tail of a manual /compact, run regardless
// of whether it succeeded or failed: clears compacting, reports the outcome,
// and drains anything the user queued while it was running. Caller must hold
// t.mu.
//
// compacting must be cleared BEFORE drainQueueLocked: combineAndDisplayQueuedLocked
// (which it calls) refuses to drain while compacting is still true — the same
// guard submit() uses to reject a message outright rather than queue it. A
// message typed during this manual /compact has no turn running to drain it
// via startTurn's own end-of-turn path, so without this it would sit queued
// forever.
func (t *TUI) finishManualCompactLocked(err error) {
	t.compacting.Store(false)
	if err != nil {
		if errors.Is(err, agent.ErrNothingToCompact) {
			t.scroll.appendRaw(styleSystem, "nothing to compact")
		} else {
			t.scroll.appendRaw(styleError, "compaction failed: "+err.Error())
		}
		t.markScrollDirty()
		t.drainQueueLocked()
		return
	}
	before, after := 0, 0
	if c, err := t.agent.Store().GetLastCompaction(t.sessionID); err == nil && c != nil {
		before, after = c.TokensBefore, c.TokensAfter
	}
	t.appendCompactionNoticeLocked(before, after)
	t.agent.UpdateStatus()
	t.syncHeaderFromAgentLocked()
	t.dirty.markStatus()
	t.drainQueueLocked()
}

var errQuitSentinel = errors.New("quit")

// cwdOf returns the current working directory or "" on error.
func cwdOf() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

// getenv is a tiny indirection so tests can override the environment without
// touching os.Setenv across goroutines.
var getenv = os.Getenv
