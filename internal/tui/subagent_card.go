package tui

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// layoutSubagentCard renders a compact one-line subagent widget in the spirit
// of the Grok CLI. The spinner/glyph, name, and elapsed timer are anchored on
// the LEFT so the runtime is always visible no matter how long the task is;
// the task (+ model) fills the remaining width and is truncated with an
// ellipsis. Turn count and context usage (once reported) ride the same
// segment as the timer.
//
//	⁘ explore  3.2s  3 turns  1,234 / 200,000  Explore checkout flow · glm-5.2:cloud
//	✓ explore  5.6s  4 turns  8,000 / 200,000  Explore checkout flow · glm-5.2:cloud
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
		dur = formatDuration(blockElapsedMs(b.meta))
	}

	// While reconnecting, the turn/context numbers are stale and a status tag
	// takes their place instead (below) — showing both would crowd the one
	// line and the numbers add nothing while the child isn't making progress.
	reconnecting := b.meta.SubagentStatus != ""

	if b.meta.SubagentTurns > 0 && !reconnecting {
		unit := "turn"
		if b.meta.SubagentTurns != 1 {
			unit = "turns"
		}
		turnsText := fmt.Sprintf("%d %s", b.meta.SubagentTurns, unit)
		if dur != "" {
			dur += "  " + turnsText
		} else {
			dur = turnsText
		}
	}

	// Context usage, formatted exactly like the main header's N / window.
	if b.meta.SubagentContextWindow > 0 && !reconnecting {
		ctxText := formatNum(b.meta.SubagentContextTokens) + " / " + formatNum(b.meta.SubagentContextWindow)
		if dur != "" {
			dur += "  " + ctxText
		} else {
			dur = ctxText
		}
	}

	// The child's own average inference speed, once it's reported one.
	if b.meta.SubagentTokensPerSec > 0 && !reconnecting {
		speedText := fmt.Sprintf("%.0f tok/s", b.meta.SubagentTokensPerSec)
		if dur != "" {
			dur += "  " + speedText
		} else {
			dur = speedText
		}
	}

	// Cost is only known once the child is done and its spend was actually
	// recorded (see completeSubagentCard/subagentCostFromResult) — never
	// shown on a still-running widget, and never fabricated as $0.0000 for a
	// run that recorded nothing (e.g. cancelled before its first billed call).
	if b.meta.ToolDone && b.meta.SubagentCostKnown {
		costText := fmt.Sprintf("$%.4f", b.meta.SubagentCost)
		if dur != "" {
			dur += "  " + costText
		} else {
			dur = costText
		}
	}

	detail := b.meta.SubagentTask
	if m := b.meta.SubagentModel; m != "" {
		if detail != "" {
			detail += " · " + m
		} else {
			detail = m
		}
	}

	// While the subagent is wrapping up after Ctrl+G, surface it in the card.
	expediteTag := ""
	if !b.meta.ToolDone && b.meta.Expediting {
		expediteTag = "⏩ wrapping up"
	}

	// While the child is retrying a network failure, show that instead of
	// (or alongside, once resolved and cleared) the ordinary tags.
	reconnectTag := ""
	if !b.meta.ToolDone && reconnecting {
		reconnectTag = "⟳ " + truncatePlain(b.meta.SubagentStatus, 40)
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
	if expediteTag != "" {
		leftPlain += "  " + expediteTag
		styledLeft += "  " + fgYellow + expediteTag + reset
	}
	if reconnectTag != "" {
		leftPlain += "  " + reconnectTag
		styledLeft += "  " + fgYellow + reconnectTag + reset
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

// dedupAdjacentWords collapses an immediately repeated word, e.g. a model that
// stutters a name field ("calc calc" -> "calc"). Distinct words are untouched
// so legitimate multi-word names ("code reviewer") survive.
func dedupAdjacentWords(s string) string {
	fields := strings.Fields(s)
	if len(fields) < 2 {
		return s
	}
	out := fields[:1]
	for _, w := range fields[1:] {
		if w != out[len(out)-1] {
			out = append(out, w)
		}
	}
	return strings.Join(out, " ")
}

// appendSubagentCard adds a running subagent widget to the scrollback.
func (s *scrollback) appendSubagentCard(id int64, providerCallID, name, task, model string) {
	b := s.newBlock(blockSubagent, "")
	b.meta = BlockMeta{
		ToolName:       dedupAdjacentWords(name),
		ProviderCallID: providerCallID,
		SubagentTask:   collapseWhitespace(task),
		SubagentModel:  model,
		Streaming:      true,
	}
	markStarted(&b.meta)
	s.blocks = append(s.blocks, b)
	s.totalAdded++
	s.trim()
}

// subagentCostRe extracts the dollar figure subagent.go's Execute appends to
// its own result text — " Cost: $0.0071." — right after "N tool calls, N
// turns.". That sentence is the only place a subagent's recorded spend
// exists once its ephemeral child DB is gone, so the widget reads it back out
// instead of threading a separate cost value through ToolResult/OutputEvent
// for what both the direct and batched completion paths already carry as
// plain text (see agent.go's tool_result dispatch and CompleteBatchedSubagent).
var subagentCostRe = regexp.MustCompile(`Cost: \$(\d+\.\d+)\.`)

// subagentCostFromResult reports the cost a finished subagent's result text
// carries, and whether one was found at all — absent (ok=false) means the run
// recorded nothing (e.g. cancelled before its first billed call), which must
// stay invisible rather than render as a fabricated $0.0000.
func subagentCostFromResult(content string) (cost float64, ok bool) {
	m := subagentCostRe.FindStringSubmatch(content)
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// subagentRanOnRe extracts the "Ran on <provider>/<model> (<effort> effort)."
// sentence subagent.go's Execute appends to its own result text — the
// durable, authoritative record of what a finished call actually ran on
// (see that file's comment for why). The effort group is optional (a model
// with no effort knob omits the parenthetical entirely).
var subagentRanOnRe = regexp.MustCompile(`Ran on (\S+)(?: \((\w+) effort\))?\.`)

// subagentRanOnFromResult extracts the "effort · provider/model" label a
// finished subagent call actually ran on, from its own result text.
// ok=false means the text carries no such marker — an old session recorded
// before this feature existed, or a run that never reached that summary
// line (e.g. cancelled before completion) — the caller then keeps whatever
// label was set at append time instead of clobbering it with nothing.
func subagentRanOnFromResult(content string) (label string, ok bool) {
	m := subagentRanOnRe.FindStringSubmatch(content)
	if m == nil {
		return "", false
	}
	label = m[1]
	if m[2] != "" {
		label = m[2] + " · " + label
	}
	return label, true
}

// completeSubagentCard marks the matching subagent widget done and reports
// whether a widget matched. Match is by providerCallID when set, otherwise the
// most recent still-running widget. content is the tool_result text (see
// subagentCostFromResult) — pass "" when unavailable (there is then simply no
// cost to show, same as any other run that recorded nothing).
func (s *scrollback) completeSubagentCard(providerCallID, content, errMsg string, durationMs int64) bool {
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
		if cost, ok := subagentCostFromResult(content); ok {
			b.meta.SubagentCost, b.meta.SubagentCostKnown = cost, true
		}
		// Overwrite the append-time guess (main session's live model/effort
		// at spawn) with the authoritative value the child actually ran on —
		// correct even after the main session later switches models, or on
		// a resumed session where the live agent's CURRENT model would
		// otherwise silently mislabel old history.
		if label, ok := subagentRanOnFromResult(content); ok {
			b.meta.SubagentModel = label
		}
		switch {
		case durationMs < 0:
			b.meta.DurationMs = 0 // unknown (e.g. resume) — omit from display
		case durationMs > 0:
			b.meta.DurationMs = durationMs
		default:
			b.meta.DurationMs = blockElapsedMs(b.meta)
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

// updateSubagentProgress records a live turn-count + context-usage update
// from the child, keyed by the outer tool_call ID (matches
// appendSubagentCard/completeSubagentCard). A no-op if no running widget
// matches — the update may race a fast-finishing subagent's completion.
// updateSubagentProgress records a live turn-count + context-usage update, or
// (when status != "") a network-retry status to show in place of that
// turn/context line — see agent.OutputRetrying and BlockMeta.SubagentStatus.
// A non-retry update (status == "") always clears any prior status: a real
// progress report means the child is actively working again. tokensPerSec is
// the child's own token-weighted average inference speed across the rounds it
// has completed so far — the same measure the header shows for this session's
// own rounds (see avgTokensPerSec), accumulated by tools.SubagentTool.Execute
// (0 if it hasn't reported one yet). Like turns/contextTokens, callers pass
// through their last-known value
// on ticks that don't carry a fresh reading (e.g. a retry-status-only tick),
// so this always assigns rather than conditionally updating.
func (s *scrollback) updateSubagentProgress(providerCallID string, turns, contextTokens, contextWindow int, tokensPerSec float64, status string) {
	for i := len(s.blocks) - 1; i >= 0; i-- {
		b := &s.blocks[i]
		if b.kind != blockSubagent || b.meta.ProviderCallID != providerCallID {
			continue
		}
		b.meta.SubagentTurns = turns
		b.meta.SubagentContextTokens = contextTokens
		b.meta.SubagentContextWindow = contextWindow
		b.meta.SubagentTokensPerSec = tokensPerSec
		b.meta.SubagentStatus = status
		b.invalidateLayout()
		return
	}
}

// markSubagentsExpediting flags every still-running subagent widget as wrapping
// up (after the user pressed Ctrl+G) and returns how many were marked.
func (s *scrollback) markSubagentsExpediting() int {
	n := 0
	for i := range s.blocks {
		b := &s.blocks[i]
		if b.kind != blockSubagent || b.meta.ToolDone || !b.meta.Streaming {
			continue
		}
		if !b.meta.Expediting {
			b.meta.Expediting = true
			b.invalidateLayout()
		}
		n++
	}
	return n
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

// subagentTaskFromInput extracts the "task", "name", "model", and "effort"
// fields from a subagent tool-call input payload. model/effort are "" when
// the call didn't set them (i.e. it inherits the parent session's own —
// see subagentModelEffortLabel, the only caller that cares about telling
// the two cases apart).
func subagentTaskFromInput(input []byte) (name, task, model, effort string) {
	var in struct {
		Task   string `json:"task"`
		Name   string `json:"name"`
		Model  string `json:"model"`
		Effort string `json:"effort"`
	}
	_ = json.Unmarshal(input, &in)
	return in.Name, in.Task, in.Model, in.Effort
}
