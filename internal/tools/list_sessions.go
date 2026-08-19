package tools

import (
	"context"
	"encoding/json"
	"time"

	"github.com/mq37/poisson/internal/store"
)

// ListSessionsTool lists every px session recorded in the local SQLite
// store — the tool-facing equivalent of `px sessions` / the resume picker —
// so the agent can browse past sessions (e.g. before recall or
// read_messages) without shelling out.
type ListSessionsTool struct {
	store *store.Store
}

// NewListSessionsTool creates a session-listing tool backed by st.
func NewListSessionsTool(st *store.Store) *ListSessionsTool {
	return &ListSessionsTool{store: st}
}

func (t *ListSessionsTool) Name() string { return "list_sessions" }

func (t *ListSessionsTool) Description() string {
	return "List px sessions recorded in the local SQLite store, newest-updated first. Each entry has sessionId, title (if set), titleHistory (previous titles, oldest-first, omitted if never renamed), createdAt/updatedAt timestamps, provider, model, cwd, isSubagent, and messageCount. named=true restricts the list to sessions that have an explicit title, skipping ad-hoc/untitled ones. Use with read_messages to inspect a session's actual conversation."
}

func (t *ListSessionsTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"named": {"type": "boolean", "description": "Only include sessions with an explicit title (default: false, include all)"},
			"limit": {"type": "integer", "description": "Max sessions to return (default: 50, -1 for all)"}
		}
	}`)
}

type sessionListEntry struct {
	SessionID    string              `json:"sessionId"`
	Title        string              `json:"title,omitempty"`
	TitleHistory []titleHistoryEntry `json:"titleHistory,omitempty"`
	CreatedAt    string              `json:"createdAt"`
	UpdatedAt    string              `json:"updatedAt"`
	Cwd          string              `json:"cwd"`
	Provider     string              `json:"provider"`
	Model        string              `json:"model"`
	IsSubagent   bool                `json:"isSubagent"`
	MessageCount int                 `json:"messageCount"`
}

// titleHistoryEntry is a past title the session held, oldest-first, so the
// list reads as a timeline ending at the entry's current Title.
type titleHistoryEntry struct {
	Title     string `json:"title"`
	CreatedAt string `json:"createdAt"`
}

func (t *ListSessionsTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	if t.store == nil {
		return ToolResult{Error: "session store is not available in this session"}, nil
	}
	var params struct {
		Named bool `json:"named"`
		Limit int  `json:"limit"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &params); err != nil {
			return ToolResult{Error: "invalid input: " + err.Error()}, nil
		}
	}
	limit := params.Limit
	if limit == 0 {
		limit = 50
	}

	// A named filter is applied after fetching, not by the SQL LIMIT — so
	// the requested limit bounds the FILTERED result count, not the
	// pre-filter one (fetching only 50 rows then discarding untitled ones
	// could return far fewer than the caller asked for).
	fetchLimit := limit
	if params.Named {
		fetchLimit = -1
	}

	sessions, err := t.store.ListSessions(fetchLimit, 0)
	if err != nil {
		return ToolResult{Error: "list sessions: " + err.Error()}, nil
	}
	counts, err := t.store.MessageCountsBySession()
	if err != nil {
		return ToolResult{Error: "count messages: " + err.Error()}, nil
	}
	ids := make([]string, len(sessions))
	for i, s := range sessions {
		ids[i] = s.ID
	}
	titleHist, err := t.store.TitleHistoryForSessions(ids)
	if err != nil {
		return ToolResult{Error: "title history: " + err.Error()}, nil
	}

	entries := make([]sessionListEntry, 0, len(sessions))
	for _, s := range sessions {
		if params.Named && (s.Title == nil || *s.Title == "") {
			continue
		}
		e := sessionListEntry{
			SessionID:    s.ID,
			CreatedAt:    time.Unix(s.CreatedAt, 0).Format(time.RFC3339),
			UpdatedAt:    time.Unix(s.UpdatedAt, 0).Format(time.RFC3339),
			Cwd:          s.Cwd,
			Provider:     s.Provider,
			Model:        s.Model,
			IsSubagent:   s.IsSubagent,
			MessageCount: counts[s.ID],
		}
		if s.Title != nil {
			e.Title = *s.Title
		}
		for _, h := range titleHist[s.ID] {
			e.TitleHistory = append(e.TitleHistory, titleHistoryEntry{
				Title:     h.Title,
				CreatedAt: time.Unix(h.CreatedAt, 0).Format(time.RFC3339),
			})
		}
		entries = append(entries, e)
		if params.Named && limit > 0 && len(entries) >= limit {
			break
		}
	}
	if len(entries) == 0 {
		return ToolResult{Content: "no sessions found"}, nil
	}

	data, err := json.Marshal(entries)
	if err != nil {
		return ToolResult{Error: "marshal sessions: " + err.Error()}, nil
	}
	return ToolResult{Content: string(data)}, nil
}
