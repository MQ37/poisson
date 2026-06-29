package tui

import "errors"

type paletteItem struct {
	cmd  string
	desc string
}

var paletteCommands = []paletteItem{
	{"/help", "show commands"},

	{"/new", "new session"},
	{"/sessions", "session picker"},
	{"/resume", "resume session"},
	{"/model", "model picker"},
	{"/providers", "provider picker"},
	{"/cost", "token cost breakdown"},
	{"/clear", "clear scrollback"},
	{"/search", "find in scrollback; /search <q> searches all sessions"},
	{"/compact", "compact context now"},
	{"/fork", "fork session"},
	{"/undo", "undo last turn"},
	{"/reload", "reload config"},
	{"/effort", "set reasoning effort"},
	{"/btw", "side question (floating box)"},
	{"/quit", "exit"},
}

type paletteOverlay = filterableListOverlay

func newPaletteOverlay(onRun func(string) error) *paletteOverlay {
	list := make([]filterableListItem, len(paletteCommands))
	for i, it := range paletteCommands {
		list[i] = filterableListItem{id: it.cmd, label: it.cmd, hint: it.desc}
	}
	pick := func(id string) bool {
		if onRun == nil {
			return true
		}
		err := onRun(id)
		return errors.Is(err, errQuitSentinel) || err == nil
	}
	return newFilterableListOverlay("command palette", list, "", pick, 72)
}