package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// layoutSubagentCard renders a compact one-line subagent widget in the spirit
// of the Grok CLI. The spinner/glyph, name, and elapsed timer are anchored on
// the LEFT so the runtime is always visible no matter how long the task is;
// the task (+ model) fills the remaining width and is truncated with an ellipsis.
//
//	⁘ explore  3.2s  Explore checkout flow · glm-5.2:cloud
//	✓ explore  5.6s  Explore checkout flow · glm-5.2:cloud
func layoutSubagentCard(b *Block, width int) []ScreenRow {
	if width < 12 {
		width = 12
	}
	name := b.meta.ToolName
	if name == "" {
		name = "subagent"
	}

	// Left segment: glyph + name + duration — always kept visible.
	var glyph, dur string
	statusStyle := dim
	if b.meta.ToolDone {
		glyph = "✓"
		statusStyle = fgGreen
		if b.meta.ToolError != "" {
			glyph = "✗"
			statusStyle = fgRed
		}
		// Duration is unknown after a resume (start time not persisted).
		if b.meta.DurationMs > 0 {
			dur = formatDuration(b.meta.DurationMs)
		}
	} else {
		glyph = toolCardSpinnerSlot
		elapsed := int64(0)
		if !b.meta.StartedAt.IsZero() {
			elapsed = time.Since(b.meta.StartedAt).Milliseconds()
		}
		dur = formatDuration(elapsed)
	}

	detail := b.meta.SubagentTask
	if m := b.meta.SubagentModel; m != "" {
		if detail != "" {
			detail += " · " + m
		} else {
			detail = m
		}
	}

	// left (plain) = "⁘ name  3.2s"; styled separately below.
	leftPlain := glyph + " " + name
	if dur != "" {
		leftPlain += "  " + dur
	}
	styledLeft := statusStyle + glyph + reset + " " + fgCyan + bold + name + reset
	if dur != "" {
		styledLeft += "  " + statusStyle + dur + reset
	}

	gap := 2
	avail := width - visibleWidth(leftPlain) - gap
	line := styledLeft
	if avail >= 4 && detail != "" {
		detail = truncatePlain(detail, avail)
		line += strings.Repeat(" ", gap) + dim + detail + reset
	}
	return []ScreenRow{{Text: line, Tag: RowTag{BlockID: b.id, RowIdx: 0}}}
}

// formatDuration renders milliseconds as a compact "1.2s" / "45s" / "2m03s".
func formatDuration(ms int64) string {
	if ms <= 0 {
		return "0.0s"
	}
	sec := float64(ms) / 1000
	if sec < 60 {
		return fmt.Sprintf("%.1fs", sec)
	}
	m := int(sec) / 60
	s := int(sec) % 60
	return fmt.Sprintf("%dm%02ds", m, s)
}

// appendSubagentCard adds a running subagent widget to the scrollback.
func (s *scrollback) appendSubagentCard(id int64, providerCallID, name, task, model string) {
	b := s.newBlock(blockSubagent, "")
	b.meta = BlockMeta{
		ToolName:       name,
		ProviderCallID: providerCallID,
		SubagentTask:   collapseWhitespace(task),
		SubagentModel:  model,
		Streaming:      true,
		StartedAt:      time.Now(),
	}
	s.blocks = append(s.blocks, b)
	s.totalAdded++
	s.trim()
}

// completeSubagentCard marks the matching subagent widget done and reports
// whether a widget matched. Match is by providerCallID when set, otherwise the
// most recent still-running widget.
func (s *scrollback) completeSubagentCard(providerCallID, errMsg string, durationMs int64) bool {
	for i := len(s.blocks) - 1; i >= 0; i-- {
		b := &s.blocks[i]
		if b.kind != blockSubagent || b.meta.ToolDone {
			continue
		}
		if providerCallID != "" && b.meta.ProviderCallID != providerCallID {
			continue
		}
		b.meta.Streaming = false
		b.meta.ToolDone = true
		b.meta.ToolError = errMsg
		switch {
		case durationMs < 0:
			b.meta.DurationMs = 0 // unknown (e.g. resume) — omit from display
		case durationMs > 0:
			b.meta.DurationMs = durationMs
		case !b.meta.StartedAt.IsZero():
			b.meta.DurationMs = time.Since(b.meta.StartedAt).Milliseconds()
		}
		b.invalidateLayout()
		return true
	}
	return false
}

// finalizeOrphanSubagents marks any still-running subagent widgets done after
// a resume (their tool_result may be missing from the replayed history).
func (s *scrollback) finalizeOrphanSubagents() {
	for i := range s.blocks {
		b := &s.blocks[i]
		if b.kind != blockSubagent || b.meta.ToolDone {
			continue
		}
		b.meta.Streaming = false
		b.meta.ToolDone = true
		b.meta.DurationMs = 0 // unknown after resume
		b.invalidateLayout()
	}
}

// hasRunningSubagent reports whether any subagent widget is still running.
func (s *scrollback) hasRunningSubagent() bool {
	for i := range s.blocks {
		if s.blocks[i].kind == blockSubagent && s.blocks[i].meta.Streaming {
			return true
		}
	}
	return false
}

// maxPinnedSubagents caps how many running subagents are pinned above the convo.
const maxPinnedSubagents = 5

// runningSubagentCount returns the number of currently-running subagents (capped).
func (s *scrollback) runningSubagentCount() int {
	n := 0
	for i := range s.blocks {
		if s.blocks[i].kind == blockSubagent && s.blocks[i].meta.Streaming {
			n++
			if n >= maxPinnedSubagents {
				break
			}
		}
	}
	return n
}

// runningSubagentLines renders the currently-running subagent widgets (capped)
// for the pinned region shown above the conversation.
func (s *scrollback) runningSubagentLines(width int) []string {
	var lines []string
	for i := range s.blocks {
		b := &s.blocks[i]
		if b.kind != blockSubagent || !b.meta.Streaming {
			continue
		}
		if rows := layoutSubagentCard(b, width); len(rows) > 0 {
			lines = append(lines, rows[0].Text)
		}
		if len(lines) >= maxPinnedSubagents {
			break
		}
	}
	return lines
}

// collapseWhitespace flattens newlines/tabs/runs of spaces into single spaces
// so a multi-line task renders on one widget line.
func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// subagentTaskFromInput extracts the "task" and "name" fields from a subagent
// tool-call input payload.
func subagentTaskFromInput(input []byte) (name, task string) {
	var in struct {
		Task string `json:"task"`
		Name string `json:"name"`
	}
	_ = json.Unmarshal(input, &in)
	return in.Name, in.Task
}
