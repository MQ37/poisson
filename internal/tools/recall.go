package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mq37/poisson/internal/store"
)

// RecallTool lets the agent search previous conversations via FTS5.
type RecallTool struct {
	store *store.Store
}

// NewRecallTool creates a conversation search tool.
func NewRecallTool(st *store.Store) *RecallTool {
	return &RecallTool{store: st}
}

func (t *RecallTool) Name() string { return "recall" }

func (t *RecallTool) Description() string {
	return "Search previous conversations using full-text search. Returns matching messages with session ID, role, and text snippet. Use to find what was discussed in past sessions."
}

func (t *RecallTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {"type": "string", "description": "Search query (natural language or keywords)"},
			"limit": {"type": "integer", "description": "Max results (default: 10)"}
		},
		"required": ["query"]
	}`)
}

func (t *RecallTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	var params struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return ToolResult{Error: "invalid input: " + err.Error()}, nil
	}
	if params.Query == "" {
		return ToolResult{Error: "query is required"}, nil
	}
	if params.Limit == 0 {
		params.Limit = 10
	}

	results, err := t.store.Search(params.Query, params.Limit)
	if err != nil {
		return ToolResult{Error: "search failed: " + err.Error()}, nil
	}
	if len(results) == 0 {
		return ToolResult{Content: "No previous conversations found matching: " + params.Query}, nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d result(s):\n\n", len(results)))
	for _, r := range results {
		sb.WriteString(fmt.Sprintf("[session: %s] %s:\n  %s\n\n", store.DisplaySessionID(r.SessionID), r.Role, r.Snippet))
	}
	return ToolResult{Content: sb.String()}, nil
}
