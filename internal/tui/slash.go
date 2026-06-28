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
		return nil
	case "/help", "/h", "/?":
		t.scroll.appendRaw(styleSystem, renderHelp())
		t.markScrollDirty()
		return nil
	case "/new":
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
			t.openSearch()
			return nil
		}
		return cmdSearch(h, parts[1:])
	case "/fork":
		return cmdFork(h, parts[1:])
	case "/undo":
		return cmdUndo(h)
	case "/compact":
		t.scroll.appendRaw(styleSystem, "/compact — manual compaction not yet available (auto-compaction handles this)")
		t.markScrollDirty()
		return nil
	case "/model":
		if len(parts) == 1 {
			t.openModelPicker()
			return nil
		}
		return cmdModel(h, parts[1:])
	case "/providers":
		t.openProviderPicker()
		return nil
	case "/effort":
		return cmdEffort(h, parts[1:])
	case "/models":
		t.openModelPicker()
		return nil
	case "/reload":
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
