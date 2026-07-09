package tui

import (
	"encoding/json"
	"strings"
)

// msgBlock mirrors agent content block JSON in the messages table.
type msgBlock struct {
	Type        string          `json:"type"`
	Text        string          `json:"text,omitempty"`
	ToolCallID  string          `json:"tool_call_id,omitempty"`
	ToolName    string          `json:"tool_name,omitempty"`
	ToolInput   json.RawMessage `json:"tool_input,omitempty"`
	ToolResult  string          `json:"tool_result,omitempty"`
	ToolIsError bool            `json:"tool_is_error,omitempty"`
	Thinking    string          `json:"thinking,omitempty"`
	Redacted    bool            `json:"redacted,omitempty"`
	FileRef     string          `json:"file_ref,omitempty"`
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
			// A message's text may be split across several adjacent blocks — e.g. a
			// literal @path reference was expanded to its own block (FileRef set) at
			// send time, isolating its content so it renders as a collapsible card
			// (below) instead of dumping the file inline. Concatenate directly (no
			// added separator): any intentional whitespace between two adjacent
			// tokens, e.g. "@a.go @b.go", already lives inside a plain-text block's
			// own Text and must not be lost the way a whitespace-only-block filter
			// would lose it.
			var parts []string
			var fileRefs []msgBlock
			for _, b := range blocks {
				if b.Type != "text" || b.Text == "" {
					continue
				}
				if b.FileRef != "" {
					// Reconstruct the literal @path token the user typed — its display
					// placeholder, not the fenced file dump, which appears as its own
					// card below instead.
					parts = append(parts, "@"+b.FileRef)
					fileRefs = append(fileRefs, b)
					continue
				}
				parts = append(parts, b.Text)
			}
			if len(parts) > 0 {
				t.scroll.append(StyledLine{Style: styleUser, Text: strings.Join(parts, "")})
			}
			for _, b := range fileRefs {
				id := nextToolID
				nextToolID++
				t.scroll.appendFileRefCard(id, b.FileRef, stripFence(b.Text))
			}
		case "assistant":
			for _, b := range blocks {
				switch b.Type {
				case "thinking":
					if b.Redacted {
						t.scroll.appendThinkingRedacted()
					} else if b.Thinking != "" {
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
					// Subagents replay as the same compact widget shown live, not a
					// full tool card, so resume matches the live view.
					if b.ToolName == "subagent" {
						name, task := subagentTaskFromInput(input)
						if name == "" {
							name = "subagent"
						}
						t.scroll.appendSubagentCard(id, b.ToolCallID, name, task, modelLabel(t.agent))
					} else {
						t.scroll.appendToolCallReplay(id, b.ToolCallID, b.ToolName, input)
					}
				}
			}
		case "tool":
			for _, b := range blocks {
				if b.Type == "tool_result" {
					content, errMsg := parseHydratedToolResult(b)
					// Try the subagent widget first; only fall back to a tool card
					// (which appends an orphan line if unmatched) when it isn't one.
					if !t.scroll.completeSubagentCard(b.ToolCallID, errMsg, -1) {
						t.scroll.completeToolCall(b.ToolCallID, content, errMsg, 0)
					}
				}
			}
		}
	}
	t.scroll.finalizeOrphanToolCalls()
	t.scroll.finalizeOrphanSubagents()
	t.scroll.finalizeThinking()
	t.scroll.scrollToBottom()
}
