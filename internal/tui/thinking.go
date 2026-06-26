package tui

import (
	"fmt"
	"strings"
	"time"
)

const thinkingStreamMarker = "▾ thinking"

// formatThinkingCollapsed renders the one-line collapsed thinking header.
func formatThinkingCollapsed(charCount int, durationMs int64) string {
	chars := formatNum(charCount)
	dur := formatThinkingDuration(durationMs)
	return dim + italic + "▸ thinking (" + chars + " chars, " + dur + ")" + reset
}

// formatThinkingStreaming renders the streaming thinking status line.
func formatThinkingStreaming() string {
	return dim + italic + thinkingStreamMarker + "… " + toolCardSpinnerSlot + reset
}

func formatThinkingDuration(ms int64) string {
	if ms < 1000 {
		if ms <= 0 {
			return "0.0s"
		}
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}

// layoutThinking renders a thinking block (collapsed, streaming, or expanded).
func layoutThinking(b *Block, width int, _ int) []ScreenRow {
	prefix := kindStylePrefix(blockThinking)
	if b.meta.Collapsed && !b.meta.Streaming {
		text := prefix + formatThinkingCollapsed(len([]rune(b.raw)), b.meta.DurationMs) + reset
		return []ScreenRow{{Text: text, Tag: RowTag{BlockID: b.id, RowIdx: 0}}}
	}
	var chunks []string
	if b.meta.Streaming {
		chunks = append(chunks, prefix+formatThinkingStreaming()+reset)
	}
	if b.raw != "" {
		md := layoutRichMarkdown(b.raw, width, "")
		if b.meta.Streaming {
			for _, ln := range md {
				chunks = append(chunks, ln)
			}
		} else {
			chunks = append(chunks, layoutRichMarkdown(b.raw, width, prefix)...)
		}
	} else if !b.meta.Streaming {
		chunks = []string{prefix + reset}
	}
	rows := make([]ScreenRow, len(chunks))
	for i, chunk := range chunks {
		rows[i] = ScreenRow{Text: chunk, Tag: RowTag{BlockID: b.id, RowIdx: i}}
	}
	return rows
}

// finalizeThinking marks the tail thinking block complete and collapsed.
func (s *scrollback) finalizeThinking() {
	if len(s.blocks) == 0 {
		return
	}
	tail := &s.blocks[len(s.blocks)-1]
	if tail.kind != blockThinking || !tail.meta.Streaming {
		return
	}
	tail.meta.Streaming = false
	tail.meta.Collapsed = true
	if !tail.meta.StartedAt.IsZero() {
		tail.meta.DurationMs = time.Since(tail.meta.StartedAt).Milliseconds()
	}
	tail.invalidateLayout()
}

// markThinkingStreaming sets streaming state on the tail thinking block.
func (s *scrollback) markThinkingStreaming() {
	if len(s.blocks) == 0 {
		return
	}
	tail := &s.blocks[len(s.blocks)-1]
	if tail.kind != blockThinking {
		return
	}
	tail.meta.Streaming = true
	if tail.meta.StartedAt.IsZero() {
		tail.meta.StartedAt = time.Now()
	}
	tail.meta.Collapsed = false
	tail.invalidateLayout()
}

// toggleThinkingInView toggles collapse on the last thinking block in the viewport.
func (s *scrollback) toggleThinkingInView(height, width int) bool {
	if height < 1 || width < 1 || len(s.blocks) == 0 {
		return false
	}
	wrapped, cumulative := s.layoutAll(width)
	if len(wrapped) == 0 {
		return false
	}
	end := len(wrapped)
	start := end - height
	if start < 0 {
		start = 0
	}
	if s.scrollTop > 0 {
		logicalEnd := len(cumulative)
		target := logicalEnd - s.scrollTop
		if target < 0 {
			target = 0
		}
		wrappedEnd := 0
		if target < len(cumulative) {
			wrappedEnd = cumulative[target]
		} else {
			wrappedEnd = len(wrapped)
		}
		end = wrappedEnd
		start = end - height
		if start < 0 {
			start = 0
		}
	}
	seen := map[int64]struct{}{}
	for i := end - 1; i >= start; i-- {
		if i < 0 || i >= len(wrapped) {
			continue
		}
		id := wrapped[i].Tag.BlockID
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		for j := range s.blocks {
			if s.blocks[j].id != id || s.blocks[j].kind != blockThinking {
				continue
			}
			if s.blocks[j].meta.Streaming {
				return false
			}
			s.blocks[j].meta.Collapsed = !s.blocks[j].meta.Collapsed
			s.blocks[j].invalidateLayout()
			return true
		}
	}
	return false
}

func thinkingSpinnerRows(visible []ScreenRow) []int {
	var rows []int
	for i, row := range visible {
		if strings.Contains(stripANSI(row.Text), thinkingStreamMarker) {
			rows = append(rows, i)
		}
	}
	return rows
}