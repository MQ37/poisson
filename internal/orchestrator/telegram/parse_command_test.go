package telegram

import (
	"testing"

	"github.com/mq37/poisson/internal/orchestrator"
)

// TestParseCommand_NewVerbs checks /new, /new-box, and /new-host all map to
// the expected CommandKind — the host-mode addendum to commandVerbs (see
// docs/orchestrator-host-mode-plan.md §3.5).
func TestParseCommand_NewVerbs(t *testing.T) {
	cases := []struct {
		text string
		want orchestrator.CommandKind
	}{
		{"/new alpha", orchestrator.CmdNew},
		{"/new-box alpha", orchestrator.CmdNewBox},
		{"/new-host alpha --confirm-unconfined-host", orchestrator.CmdNewHost},
	}
	for _, c := range cases {
		msg := &Message{From: User{ID: 111}, Chat: Chat{ID: -100}, Text: c.text}
		got := parseCommand(msg, -100)
		if got.Kind != c.want {
			t.Errorf("parseCommand(%q).Kind = %v, want %v", c.text, got.Kind, c.want)
		}
	}
	// /new and /new-box are the same underlying value — the whole point of
	// the alias.
	if orchestrator.CmdNew != orchestrator.CmdNewBox {
		t.Error("CmdNew != CmdNewBox — the alias contract broke")
	}
}
