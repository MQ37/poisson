package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mq37/poisson/internal/store"
)

// ReadMessagesTool reads back a session's actual conversation history by
// id — the complement to recall (FTS search over snippets) and
// list_sessions (which only surfaces metadata): once one of those points at
// a session id, this reads its messages. Uses GetAllMessages (active AND
// compacted turns, oldest first) so a session's full history is visible
// even after compaction dropped older turns from what's actually sent to
// the model.
type ReadMessagesTool struct {
	store *store.Store
}

// NewReadMessagesTool creates a message-reading tool backed by st.
func NewReadMessagesTool(st *store.Store) *ReadMessagesTool {
	return &ReadMessagesTool{store: st}
}

func (t *ReadMessagesTool) Name() string { return "read_messages" }

func (t *ReadMessagesTool) Description() string {
	return "Read messages from a px session by id, oldest first, including turns dropped by compaction. Returns seq, role, createdAt, compacted flag, and rendered content (text, tool calls, tool results, thinking) per message. limit/offset paginate a long conversation (default limit: 50, -1 for all; default offset: 0). Use list_sessions or recall first to find the session id."
}

func (t *ReadMessagesTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"id": {"type": "string", "description": "Session id, e.g. s-a3f9c1d2"},
			"limit": {"type": "integer", "description": "Max messages to return (default: 50, -1 for all)"},
			"offset": {"type": "integer", "description": "Skip this many messages from the start (default: 0)"}
		},
		"required": ["id"]
	}`)
}

type messageEntry struct {
	Seq       int    `json:"seq"`
	Role      string `json:"role"`
	CreatedAt string `json:"createdAt"`
	Compacted bool   `json:"compacted,omitempty"`
	Content   string `json:"content"`
}

func (t *ReadMessagesTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	if t.store == nil {
		return ToolResult{Error: "session store is not available in this session"}, nil
	}
	var params struct {
		ID     string `json:"id"`
		Limit  int    `json:"limit"`
		Offset int    `json:"offset"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return ToolResult{Error: "invalid input: " + err.Error()}, nil
	}
	if params.ID == "" {
		return ToolResult{Error: "id is required"}, nil
	}
	if _, err := t.store.GetSession(params.ID); err != nil {
		if err == store.ErrNotFound {
			return ToolResult{Error: "no session with id " + params.ID}, nil
		}
		return ToolResult{Error: "get session: " + err.Error()}, nil
	}

	msgs, err := t.store.GetAllMessages(params.ID)
	if err != nil {
		return ToolResult{Error: "read messages: " + err.Error()}, nil
	}
	msgs = paginateMessages(msgs, params.Limit, params.Offset)
	if len(msgs) == 0 {
		return ToolResult{Content: "no messages found"}, nil
	}

	entries := make([]messageEntry, 0, len(msgs))
	for _, m := range msgs {
		entries = append(entries, messageEntry{
			Seq:       m.Seq,
			Role:      m.Role,
			CreatedAt: time.Unix(m.CreatedAt, 0).Format(time.RFC3339),
			Compacted: m.Compacted,
			Content:   renderMessageContent(m.Content),
		})
	}
	data, err := json.Marshal(entries)
	if err != nil {
		return ToolResult{Error: "marshal messages: " + err.Error()}, nil
	}
	return ToolResult{Content: string(data)}, nil
}

// paginateMessages slices msgs to [offset, offset+limit). limit == 0 defaults
// to 50; limit < 0 means "no cap" (return everything from offset on).
// offset beyond the slice, or offset < 0, yields an empty (never negative-
// indexed) result rather than panicking.
func paginateMessages(msgs []store.Message, limit, offset int) []store.Message {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(msgs) {
		return nil
	}
	msgs = msgs[offset:]
	if limit == 0 {
		limit = 50
	}
	if limit > 0 && limit < len(msgs) {
		msgs = msgs[:limit]
	}
	return msgs
}

// msgBlock mirrors the JSON shape a content block is persisted in (see
// agent.contentBlockJSON, the writer side — snake_case keys, unexported,
// package-private on both ends so each reader/writer pair states its own
// copy rather than sharing a type across package boundaries; tui.msgBlock
// is the same pattern for the TUI's own history rendering).
type msgBlock struct {
	Type       string          `json:"type"`
	Text       string          `json:"text,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	ToolInput  json.RawMessage `json:"tool_input,omitempty"`
	ToolResult string          `json:"tool_result,omitempty"`
	Thinking   string          `json:"thinking,omitempty"`
	ImageName  string          `json:"image_name,omitempty"`
}

// renderMessageContent turns a message's stored content JSON (a []msgBlock
// array) into a compact human-readable string: text verbatim, tool calls as
// "name(input)", tool results and thinking labeled and inlined. Falls back
// to the raw stored string if it doesn't parse as blocks (defensive; every
// row is written by AppendMessage in this shape).
func renderMessageContent(raw string) string {
	var blocks []msgBlock
	if err := json.Unmarshal([]byte(raw), &blocks); err != nil {
		return raw
	}
	rendered := make([]string, 0, len(blocks))
	for _, b := range blocks {
		switch b.Type {
		case "text":
			rendered = append(rendered, b.Text)
		case "tool_use":
			rendered = append(rendered, fmt.Sprintf("[tool_use %s(%s)]", b.ToolName, string(b.ToolInput)))
		case "tool_result":
			rendered = append(rendered, fmt.Sprintf("[tool_result: %s]", b.ToolResult))
		case "thinking":
			rendered = append(rendered, "[thinking: "+b.Thinking+"]")
		case "image":
			rendered = append(rendered, fmt.Sprintf("[image: %s]", b.ImageName))
		default:
			rendered = append(rendered, "["+b.Type+"]")
		}
	}
	out := ""
	for i, r := range rendered {
		if i > 0 {
			out += "\n"
		}
		out += r
	}
	return out
}
