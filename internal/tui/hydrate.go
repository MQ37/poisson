package tui

import "encoding/json"

// msgBlock mirrors agent content block JSON in the messages table.
type msgBlock struct {
	Type       string          `json:"type"`
	Text       string          `json:"text,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	ToolInput  json.RawMessage `json:"tool_input,omitempty"`
	ToolResult string          `json:"tool_result,omitempty"`
	Thinking   string          `json:"thinking,omitempty"`
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
			for _, b := range blocks {
				if b.Type == "text" && b.Text != "" {
					t.scroll.append(StyledLine{Style: styleUser, Text: b.Text})
				}
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
					t.scroll.appendToolCall(id, b.ToolCallID, b.ToolName, input)
				}
			}
		case "tool":
			for _, b := range blocks {
				if b.Type == "tool_result" {
					t.scroll.completeToolCall(b.ToolCallID, b.ToolResult, "", 0)
				}
			}
		}
	}
	t.scroll.finalizeThinking()
	t.scroll.scrollToBottom()
}