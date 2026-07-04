package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// layoutSubagentCard renders a compact one-line subagent widget in the spirit
// of the Grok CLI: an icon, the agent name, a truncated task, the model label,
// and a live status (spinner + elapsed while running; ✓/✗ + duration when done).
//
//	⁘ explore   Explore checkout flow · glm-5.2:cloud            ◌ 5.6s
//	⁘ explore   Explore checkout flow · glm-5.2:cloud            ✓ 5.6s
func layoutSubagentCard(b *Block, width int) []ScreenRow {
	if width < 12 {
		width = 12
	}
	name := b.meta.ToolName
	if name == "" {
		name = "subagent"
	}

	// Status glyph + elapsed/duration.
	var status string
	if b.meta.ToolDone {
		glyph := "✓"
		if b.meta.ToolError != "" {
			glyph = "✗"
		}
		status = fmt.Sprintf("%s %s", glyph, formatDuration(b.meta.DurationMs))
	} else {
		elapsed := int64(0)
		if !b.meta.StartedAt.IsZero() {
			elapsed = time.Since(b.meta.StartedAt).Milliseconds()
		}
		status = fmt.Sprintf("%s %s", toolCardSpinnerSlot, formatDuration(elapsed))
	}

	icon := "⁘ "
	label := icon + name
	detail := b.meta.SubagentTask
	if m := b.meta.SubagentModel; m != "" {
		if detail != "" {
			detail += " · " + m
		} else {
			detail = m
		}
	}

	// Colorize: name in cyan, detail dim, status green/red/dim.
	nameStyle := fgCyan + bold
	statusStyle := dim
	if b.meta.ToolDone {
		if b.meta.ToolError != "" {
			statusStyle = fgRed
		} else {
			statusStyle = fgGreen
		}
	}

	// Budget the detail so name + detail + status fit on one line.
	// Layout: "<label>  <detail><pad><status>"
	labelW := visibleWidth(label)
	statusW := visibleWidth(status)
	gap := 2
	avail := width - labelW - gap - statusW - 1
	if avail < 4 {
		avail = 4
	}
	detail = truncatePlain(detail, avail)
	detailW := visibleWidth(detail)
	pad := width - labelW - gap - detailW - statusW
	if pad < 1 {
		pad = 1
	}

	line := nameStyle + label + reset +
		strings.Repeat(" ", gap) + dim + detail + reset +
		strings.Repeat(" ", pad) + statusStyle + status + reset

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

// completeSubagentCard marks the matching subagent widget done. Match is by
// providerCallID when set, otherwise the most recent still-running widget.
func (s *scrollback) completeSubagentCard(providerCallID, errMsg string, durationMs int64) {
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
		if durationMs > 0 {
			b.meta.DurationMs = durationMs
		} else if !b.meta.StartedAt.IsZero() {
			b.meta.DurationMs = time.Since(b.meta.StartedAt).Milliseconds()
		}
		b.invalidateLayout()
		return
	}
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
