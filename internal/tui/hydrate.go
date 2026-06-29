package tui

import (
	"encoding/json"
	"strings"
)

// msgBlock mirrors agent content block JSON in the messages table.
type msgBlock struct {
	Type       string          `json:"type"`
	Text       string          `json:"text,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	ToolInput  json.RawMessage `json:"tool_input,omitempty"`
	ToolResult  string `json:"tool_result,omitempty"`
	ToolIsError bool   `json:"tool_is_error,omitempty"`
	Thinking    string `json:"thinking,omitempty"`
}

func parseMessageBlocks(content string) []msgBlock {
	var blocks []msgBlock
	if err := json.Unmarshal([]byte(content), &blocks); err != nil {
		if content != "" {
			return []msgBlock{{Type: "text", Text: content}}
		}
		return nil
	}
	return blocks
}

func parseHydratedToolResult(b msgBlock) (content, errMsg string) {
	if b.ToolIsError {
		return b.ToolResult, b.ToolResult
	}
	// Legacy rows stored errors as "Error: …" prefix.
	if strings.HasPrefix(b.ToolResult, "Error: ") {
		return b.ToolResult, strings.TrimPrefix(b.ToolResult, "Error: ")
	}
	return b.ToolResult, ""
}

// refreshScrollbackFromStoreLocked rebuilds on-screen scrollback from the store.
// Caller must hold t.mu.
func (t *TUI) refreshScrollbackFromStoreLocked() {
	t.clearScrollbackKeepIntroLocked()
	t.hydrateScrollbackLocked()
	t.markFullDirty()
}

// hydrateScrollbackLocked replays store messages into scrollback. Caller holds t.mu.
func (t *TUI) hydrateScrollbackLocked() {
	if t.agent == nil {
		return
	}
	msgs, err := t.agent.Store().GetMessages(t.sessionID)
	if err != nil || len(msgs) == 0 {
		return
	}
	if sess, err := t.agent.Store().GetSession(t.sessionID); err == nil && sess != nil &&
		sess.CompactionSummary != nil && *sess.CompactionSummary != "" {
		t.scroll.appendRaw(styleSystem, "  [earlier context compacted — summary in system prompt]")
	}
	var nextToolID int64 = 1
	for _, m := range msgs {
		blocks := parseMessageBlocks(m.Content)
		switch m.Role {
		case "user":
			var parts []string
			for _, b := range blocks {
				if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
					parts = append(parts, b.Text)
				}
			}
			if len(parts) > 0 {
				t.scroll.append(StyledLine{Style: styleUser, Text: strings.Join(parts, "\n")})
			}
		case "assistant":
			for _, b := range blocks {
				switch b.Type {
				case "thinking":
					if b.Thinking != "" {
						t.scroll.append(StyledLine{Style: styleThinking, Text: b.Thinking})
					}
				case "text":
					if b.Text != "" {
						t.scroll.append(StyledLine{Style: styleAssistant, Text: b.Text})
					}
				case "tool_use":
					id := nextToolID
					nextToolID++
					input := b.ToolInput
					if len(input) == 0 {
						input = json.RawMessage("{}")
					}
					t.scroll.appendToolCallReplay(id, b.ToolCallID, b.ToolName, input)
				}
			}
		case "tool":
			for _, b := range blocks {
				if b.Type == "tool_result" {
					content, errMsg := parseHydratedToolResult(b)
					t.scroll.completeToolCall(b.ToolCallID, content, errMsg, 0)
				}
			}
		}
	}
	t.scroll.finalizeOrphanToolCalls()
	t.scroll.finalizeThinking()
	t.scroll.scrollToBottom()
}