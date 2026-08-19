package tools

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/mq37/poisson/internal/store"
)

// SetTitleTool lets the agent rename its own session's display title —
// shown in the terminal window/tab title (see internal/tui/window_title.go)
// and in list_sessions — so the user can tell windows apart at a glance as
// the task shifts. sessionIDFn/ensureFn are bound after the owning Agent
// exists (see BindSessionTitle in build.go); BuildRegistry runs first and
// has no Agent to read a live session id from yet.
type SetTitleTool struct {
	store       *store.Store
	sessionIDFn func() string
	ensureFn    func() error
}

// NewSetTitleTool creates a session-title tool backed by st. Call
// SetSessionFns before use — see BindSessionTitle.
func NewSetTitleTool(st *store.Store) *SetTitleTool {
	return &SetTitleTool{store: st}
}

// SetSessionFns wires the live current-session-id getter and the
// session-row-exists guarantee, once the owning Agent is constructed.
func (t *SetTitleTool) SetSessionFns(sessionIDFn func() string, ensureFn func() error) {
	t.sessionIDFn = sessionIDFn
	t.ensureFn = ensureFn
}

func (t *SetTitleTool) Name() string { return "set_title" }

func (t *SetTitleTool) Description() string {
	return "Set this session's display title — shown in the terminal window/tab title and in list_sessions, so you and the user stay oriented on what this window is doing. Keep it to a few words (e.g. 'pr 123 tools refactor', 'fix auth bug'). Call it whenever the task shifts to something new and identifiable; title changes are kept as history, so renaming as the task evolves is fine."
}

func (t *SetTitleTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"title": {"type": "string", "description": "Short display title, a few words"}
		},
		"required": ["title"]
	}`)
}

func (t *SetTitleTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	if t.store == nil || t.sessionIDFn == nil || t.ensureFn == nil {
		return ToolResult{Error: "set_title is not available in this session"}, nil
	}
	var params struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return ToolResult{Error: "invalid input: " + err.Error()}, nil
	}
	title := strings.TrimSpace(params.Title)
	if title == "" {
		return ToolResult{Error: "title is required"}, nil
	}

	if err := t.ensureFn(); err != nil {
		return ToolResult{Error: "ensure session: " + err.Error()}, nil
	}
	if err := t.store.SetSessionTitle(t.sessionIDFn(), title); err != nil {
		return ToolResult{Error: "set title: " + err.Error()}, nil
	}
	return ToolResult{Content: "title set: " + title}, nil
}
