package tui

import (
	"errors"
	"os"
	"strings"
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
		t.scroll = newScrollback(8192)
		t.markFullDirty()
		t.scroll.appendRaw(styleSystem, "display cleared (session history unchanged)")
		t.markScrollDirty()
		return nil
	case "/help", "/h", "/?":
		t.scroll.appendRaw(styleSystem, renderHelp())
		t.markScrollDirty()
		return nil
	case "/new":
		if t.running() {
			t.scroll.appendRaw(styleSystem, "cannot create session while agent is running")
			t.markScrollDirty()
			return nil
		}
		return cmdNew(h)
	case "/resume", "/r":
		if len(parts) == 1 {
			t.openSessionPicker()
			return nil
		}
		return cmdResume(h, parts[1:])
	case "/sessions":
		t.openSessionPicker()
		return nil
	case "/search":
		if len(parts) == 1 {
			t.openSearchLocked()
			return nil
		}
		return cmdSearch(h, parts[1:])
	case "/fork":
		if t.running() {
			t.scroll.appendRaw(styleSystem, "cannot fork while agent is running")
			t.markScrollDirty()
			return nil
		}
		return cmdFork(h, parts[1:])
	case "/undo":
		if t.running() {
			t.scroll.appendRaw(styleSystem, "cannot undo while agent is running")
			t.markScrollDirty()
			return nil
		}
		return cmdUndo(h)
	case "/compact":
		if t.running() {
			t.scroll.appendRaw(styleSystem, "cannot compact while agent is running")
			t.markScrollDirty()
			return nil
		}
		t.scroll.appendRaw(styleSystem, "  compacting context...")
		t.markScrollDirty()
		go func() {
			err := t.agent.Compact()
			t.mu.Lock()
			defer t.mu.Unlock()
			if err != nil {
				t.scroll.appendRaw(styleError, "compaction failed: "+err.Error())
				t.markScrollDirty()
				return
			}
			t.scroll = newScrollback(8192)
			t.hydrateScrollbackLocked()
			t.markFullDirty()
		}()
		return nil
	case "/model":
		if len(parts) == 1 {
			if t.running() {
				t.scroll.appendRaw(styleSystem, "cannot change model while agent is running")
				t.markScrollDirty()
				return nil
			}
			t.openModelPicker()
			return nil
		}
		if t.running() {
			t.scroll.appendRaw(styleSystem, "cannot change model while agent is running")
			t.markScrollDirty()
			return nil
		}
		return cmdModel(h, parts[1:])
	case "/providers":
		if t.running() {
			t.scroll.appendRaw(styleSystem, "cannot change provider while agent is running")
			t.markScrollDirty()
			return nil
		}
		t.openProviderPicker()
		return nil
	case "/effort":
		if t.running() && len(parts) > 1 {
			t.scroll.appendRaw(styleSystem, "cannot change effort while agent is running")
			t.markScrollDirty()
			return nil
		}
		return cmdEffort(h, parts[1:])
	case "/reload":
		if t.running() {
			t.scroll.appendRaw(styleSystem, "cannot reload while agent is running")
			t.markScrollDirty()
			return nil
		}
		return cmdReload(h)
	case "/cost":
		cmdCost(h)
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
