package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	MediaType   string          `json:"media_type,omitempty"`
	ImagePath   string          `json:"image_path,omitempty"`
	ImageName   string          `json:"image_name,omitempty"`
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
//
// Loads the FULL history (GetAllMessages), not just what survived the most
// recent /compact (GetMessages, compacted = 0 only) — compaction never
// deletes a message, it only flags it, so the detail is always still in the
// store. Resuming a compacted session used to show a single opaque
// placeholder line and nothing else; now the real conversation renders in
// full, with a boundary marker (plus the actual summary text) at the exact
// point where the model's active context now starts. What the agent sends
// the model is unaffected — buildRequest still calls GetMessages.
func (t *TUI) hydrateScrollbackLocked() {
	if t.agent == nil {
		return
	}
	msgs, err := t.agent.Store().GetAllMessages(t.sessionID)
	if err != nil || len(msgs) == 0 {
		return
	}
	var summary string
	if sess, err := t.agent.Store().GetSession(t.sessionID); err == nil && sess != nil && sess.CompactionSummary != nil {
		summary = *sess.CompactionSummary
	}
	bannerShown := false
	showCompactionBanner := func() {
		t.scroll.appendRaw(styleSystem, "  ── compacted here — messages above are history only; the model now starts from this summary ──")
		for _, ln := range wrapPlain(summary, 100) {
			t.scroll.appendRaw(styleSystem, "  "+dim+ln+reset)
		}
		bannerShown = true
	}
	var nextToolID int64 = 1
	for i, m := range msgs {
		// The boundary sits right before the first still-active message —
		// everything at or before it was marked compacted by the most recent
		// /compact. If every message in the session is compacted (just ran
		// /compact with nothing sent since), the fallback after the loop
		// covers it instead.
		if summary != "" && !bannerShown && !m.Compacted && (i == 0 || msgs[i-1].Compacted) {
			showCompactionBanner()
		}
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
			var images []msgBlock
			for _, b := range blocks {
				if b.Type == "image" && b.ImagePath != "" {
					images = append(images, b)
					continue
				}
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
			// Order matches the live submit path (agent_io.go): text bubble, then
			// file-ref cards, then image cards.
			if len(parts) > 0 {
				t.scroll.append(StyledLine{Style: styleUser, Text: strings.Join(parts, "")})
			}
			for _, b := range fileRefs {
				id := nextToolID
				nextToolID++
				t.scroll.appendFileRefCard(id, b.FileRef, stripFence(b.Text))
			}
			for _, b := range images {
				// The staged file lives in /tmp and may or may not still exist by
				// resume time — stat is best-effort for size, name always shows.
				size := 0
				if fi, err := os.Stat(b.ImagePath); err == nil {
					size = int(fi.Size())
				}
				// ImageName is the original filename the user attached/pasted;
				// only missing on rows persisted before it was added, where
				// ImagePath's random /tmp basename is the best that's left.
				name := b.ImageName
				if name == "" {
					name = filepath.Base(b.ImagePath)
				}
				id := nextToolID
				nextToolID++
				t.scroll.appendImageRefCard(id, name, b.MediaType, size)
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
	if summary != "" && !bannerShown {
		// Every message in the session is compacted (e.g. /compact just ran
		// with nothing sent since) — show the boundary and summary at the end
		// instead of never at all.
		showCompactionBanner()
	}
	t.scroll.finalizeOrphanToolCalls()
	t.scroll.finalizeOrphanSubagents()
	t.scroll.finalizeThinking()
	t.scroll.scrollToBottom()
	// Replaying history above went through the same scroll.append/appendBlock
	// path a live round uses, which marks every thinking/assistant block it
	// touches as "pending" for the NEXT applyInferenceSpeed call (see
	// scrollback.markRoundBlock) — there is no live round in progress here,
	// so that would wrongly stamp the entire replayed history with whatever
	// tok/s the first real round after resume reports. Nothing hydrated here
	// was ever part of an in-flight round; drop it.
	t.scroll.pendingSpeedBlocks = nil
}
