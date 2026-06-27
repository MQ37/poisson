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
	for _, ln := range bodyLines {
		chunks = append(chunks, fgYellow+toolCardBodyLine(ln, width)+reset)
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
	if b.meta.ToolDone && !b.meta.Expanded {
		chunks = append(chunks, toolCardResultLine(b, width)+reset)
	}
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

func toolCardBodyLine(content string, width int) string {
	inner := width - 4
	if inner < 1 {
		inner = 1
	}
	for _, chunk := range wrapLine(stripANSI(content), inner) {
		pad := inner - len([]rune(chunk))
		if pad < 0 {
			pad = 0
		}
		return "│ " + chunk + strings.Repeat(" ", pad) + " │"
	}
	return "│ " + strings.Repeat(" ", inner) + " │"
}

func toolCardFooter(width int) string {
	fill := width - 2
	if fill < 0 {
		fill = 0
	}
	return "╰" + strings.Repeat("─", fill) + "╯"
}

func toolCardBody(toolName string, input []byte, width int) []string {
	preview := toolInputPreview(toolName, input)
	inner := width - 4
	if inner < 1 {
		inner = 1
	}
	return wrapLine(preview, inner)
}

func toolCardResultLine(b *Block, width int) string {
	text := toolResultFullText(b)
	preview := previewText(text, toolResultCollapsedBytes)
	inner := width - 8
	if inner < 10 {
		inner = width - 4
	}
	lines := wrapLine(preview, inner)
	if len(lines) > toolResultCollapsedLines {
		lines = lines[:toolResultCollapsedLines]
	}
	body := strings.Join(lines, " ")
	var summary string
	if b.meta.ToolError != "" {
		summary = "  ✗ " + body
	} else {
		summary = "  ✓ " + body
	}
	if b.meta.DurationMs > 0 {
		summary += fmt.Sprintf(" · %.1fs", float64(b.meta.DurationMs)/1000)
	}
	hint := ""
	if toolResultNeedsExpand(b) {
		hint = dim + " · click/Ctrl+E" + reset
	}
	avail := width - visibleWidth(hint)
	if avail < 12 {
		avail = width
		hint = ""
	}
	return fgGray + truncateToWidth(summary, avail) + hint + reset
}

// appendToolCall adds a tool invocation block.
func (s *scrollback) appendToolCall(id int64, providerCallID, name string, input []byte) {
	b := s.newBlock(blockToolCall, "")
	b.meta = BlockMeta{
		ToolName:       name,
		ToolID:         id,
		ProviderCallID: providerCallID,
		ToolInput:      append([]byte(nil), input...),
		Streaming:      true,
		StartedAt:      time.Now(),
	}
	s.blocks = append(s.blocks, b)
	s.totalAdded++
	s.lastStreamWrapCount = 0
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