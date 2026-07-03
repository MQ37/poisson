package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const toolCardSpinnerSlot = "◌" // replaced at paint time with animated spinner

// layoutToolCard renders a structured tool invocation card.
func layoutToolCard(b *Block, width int, _ int) []ScreenRow {
	if width < 12 {
		width = 12
	}
	name := b.meta.ToolName
	if name == "" {
		name = "tool"
	}
	spin := toolCardSpinnerSlot
	if b.meta.ToolDone {
		if b.meta.ToolError != "" {
			spin = "✗"
		} else {
			spin = "✓"
		}
	}
	header := toolCardHeader(name, spin, width)
	bodyLines := toolCardBody(name, b.meta.ToolInput, width)
	var chunks []string
	chunks = append(chunks, fgYellow+header+reset)
	inner := width - 4
	if inner < 1 {
		inner = 1
	}
	for _, ln := range bodyLines {
		if strings.Contains(ln, "\x1b[") {
			for _, chunk := range wrapANSI(ln, inner) {
				chunks = append(chunks, formatToolCardBodyLineANSI(chunk, width))
			}
			continue
		}
		for _, chunk := range wrapLine(stripANSI(ln), inner) {
			chunks = append(chunks, fgYellow+formatToolCardBodyLine(chunk, width)+reset)
		}
	}
	if b.meta.ToolDone && !b.meta.Expanded {
		style := fgGray
		if b.meta.ToolError != "" {
			style = fgRed
		}
		for _, ln := range toolCardCollapsedResultLines(b, width) {
			chunks = append(chunks, style+toolCardBodyLine(ln, width)+reset)
		}
	}
	if b.meta.ToolDone && b.meta.Expanded {
		for _, ln := range toolCardExpandedResultLines(b, width) {
			style := fgGray
			if b.meta.ToolError != "" {
				style = fgRed
			}
			chunks = append(chunks, style+toolCardBodyLine(ln, width)+reset)
		}
	}
	chunks = append(chunks, fgYellow+toolCardFooter(width)+reset)
	rows := make([]ScreenRow, len(chunks))
	for i, chunk := range chunks {
		rows[i] = ScreenRow{Text: chunk, Tag: RowTag{BlockID: b.id, RowIdx: i}}
	}
	return rows
}

func toolCardHeader(name, status string, width int) string {
	title := "─ " + name + " "
	fill := width - visibleWidth("╭"+title+status+" ─╮")
	if fill < 0 {
		fill = 0
	}
	return "╭" + title + strings.Repeat("─", fill) + " " + status + " ╮"
}

func formatToolCardBodyLine(chunk string, width int) string {
	inner := width - 4
	if inner < 1 {
		inner = 1
	}
	pad := inner - len([]rune(chunk))
	if pad < 0 {
		pad = 0
	}
	return "│ " + chunk + strings.Repeat(" ", pad) + " │"
}

func toolCardBodyLine(content string, width int) string {
	inner := width - 4
	if inner < 1 {
		inner = 1
	}
	chunks := wrapLine(stripANSI(content), inner)
	if len(chunks) == 0 {
		return "│ " + strings.Repeat(" ", inner) + " │"
	}
	return formatToolCardBodyLine(chunks[0], width)
}

func toolCardFooter(width int) string {
	fill := width - 2
	if fill < 0 {
		fill = 0
	}
	return "╰" + strings.Repeat("─", fill) + "╯"
}

func toolCardBody(toolName string, input []byte, width int) []string {
	if toolName == "bash" {
		var in struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(input, &in) == nil && in.Command != "" {
			inner := width - 4
			if inner < 1 {
				inner = 1
			}
			return bashToolCommandLines(in.Command, inner)
		}
	}
	preview := toolInputPreview(toolName, input)
	inner := width - 4
	if inner < 1 {
		inner = 1
	}
	return wrapLine(preview, inner)
}

func formatToolCardBodyLineANSI(content string, width int) string {
	inner := width - 4
	if inner < 1 {
		inner = 1
	}
	pad := inner - visibleWidth(content)
	if pad < 0 {
		content = truncateToWidth(content, inner)
		pad = inner - visibleWidth(content)
	}
	if pad < 0 {
		pad = 0
	}
	return fgYellow + "│ " + reset + content + strings.Repeat(" ", pad) + fgYellow + " │" + reset
}

// toolCardCollapsedResultLines returns inner (unboxed) result preview rows shown
// inside the card before the footer when the tool is done but not expanded.
func toolCardCollapsedResultLines(b *Block, width int) []string {
	text := toolResultFullText(b)
	preview := previewText(text, toolResultCollapsedBytes)
	inner := width - 4
	if inner < 1 {
		inner = 1
	}
	lines := wrapLine(preview, inner)
	if len(lines) > toolResultCollapsedLines {
		lines = lines[:toolResultCollapsedLines]
	}
	if len(lines) == 0 {
		lines = []string{""}
	}
	prefix := "✓ "
	if b.meta.ToolError != "" {
		prefix = "✗ "
	}
	lines[0] = prefix + strings.TrimSpace(lines[0])
	for i := 1; i < len(lines); i++ {
		lines[i] = "  " + strings.TrimSpace(lines[i])
	}
	last := len(lines) - 1
	suffix := ""
	if b.meta.DurationMs > 0 {
		suffix = fmt.Sprintf(" · %.1fs", float64(b.meta.DurationMs)/1000)
	}
	hint := ""
	if toolResultNeedsExpand(b) {
		hint = " · click/Ctrl+E"
	}
	avail := inner - visibleWidth(suffix+hint)
	if avail < 4 {
		avail = inner
		suffix = ""
		hint = ""
	}
	lines[last] = truncateToWidth(lines[last], avail) + suffix + hint
	return lines
}

// appendToolCall adds a tool invocation block.
func (s *scrollback) appendToolCall(id int64, providerCallID, name string, input []byte) {
	b := s.newBlock(blockToolCall, "")
	b.meta = BlockMeta{
		ToolName:       name,
		ProviderCallID: providerCallID,
		ToolInput:      append([]byte(nil), input...),
		Streaming:      true,
		StartedAt:      time.Now(),
	}
	s.blocks = append(s.blocks, b)
	s.totalAdded++
	s.trim()
}

// completeToolCall attaches a result to the matching open tool card.
// When providerCallID is set, pair by id so parallel results can arrive out of order.
// Otherwise fall back to oldest open card (FIFO).
func (s *scrollback) completeToolCall(providerCallID, result, err string, durationMs int64) {
	for i := range s.blocks {
		if s.blocks[i].kind != blockToolCall || s.blocks[i].meta.ToolDone {
			continue
		}
		if providerCallID != "" && s.blocks[i].meta.ProviderCallID != providerCallID {
			continue
		}
		b := &s.blocks[i]
		b.meta.Streaming = false
		b.meta.ToolDone = true
		b.meta.ToolResult = result
		b.meta.ToolError = err
		if durationMs > 0 {
			b.meta.DurationMs = durationMs
		} else if !b.meta.StartedAt.IsZero() {
			b.meta.DurationMs = time.Since(b.meta.StartedAt).Milliseconds()
		}
		b.invalidateLayout()
		return
	}
	// Orphan result — plain fallback line.
	s.appendRaw(styleToolResult, toolResultFallback("", result, err))
}

// finalizeOrphanToolCalls marks replayed tool cards without results as done.
func (s *scrollback) finalizeOrphanToolCalls() {
	for i := range s.blocks {
		b := &s.blocks[i]
		if b.kind != blockToolCall || b.meta.ToolDone {
			continue
		}
		b.meta.Streaming = false
		b.meta.ToolDone = true
		if b.meta.ToolResult == "" {
			b.meta.ToolResult = "(no result)"
		}
		b.invalidateLayout()
	}
}

func toolResultFallback(name, result, err string) string {
	if err != "" {
		return "  ✗ " + previewText(err, 400)
	}
	return "  ✓ " + toolResultPreview(name, result)
}

// toolCardSpinnerRows returns scroll rows containing animated tool spinners.
func toolCardSpinnerRows(visible []ScreenRow) []int {
	var rows []int
	for i, row := range visible {
		if strings.Contains(stripANSI(row.Text), toolCardSpinnerSlot) {
			rows = append(rows, i)
		}
	}
	return rows
}

// animateSpinnerInLine swaps the spinner placeholder in a line.
func animateSpinnerInLine(text string, frame int) string {
	plain := stripANSI(text)
	idx := strings.Index(plain, toolCardSpinnerSlot)
	if idx < 0 {
		return text
	}
	styleEnd := len(text) - len(plain)
	return text[:styleEnd] + plain[:idx] + spinnerChar(frame) + plain[idx+len(toolCardSpinnerSlot):]
}

// toolInputJSON is a helper for tests.
func toolInputJSON(toolName string, v any) []byte {
	b, _ := json.Marshal(v)
	return b
}