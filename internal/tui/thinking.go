package tui

import (
	"fmt"
	"strings"
	"time"
)

const thinkingStreamMarker = "▾ thinking"

// formatThinkingCollapsed renders the one-line collapsed thinking header.
// tokPerSec is the round's average output speed (see
// agent.OutputInferenceSpeed); <= 0 (unknown — still streaming, or the block
// came from a resumed session) omits it entirely.
func formatThinkingCollapsed(charCount int, durationMs int64, tokPerSec float64) string {
	chars := formatNum(charCount)
	dur := formatThinkingDuration(durationMs)
	speed := ""
	if tokPerSec > 0 {
		speed = fmt.Sprintf(", %.0f tok/s", tokPerSec)
	}
	return dim + italic + "▸ thinking (" + chars + " chars, " + dur + speed + ")" + reset
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

func formatThinkingRedactedCollapsed() string {
	return dim + italic + "▸ thinking (redacted)" + reset
}

// layoutThinking renders a thinking block (collapsed, streaming, or expanded).
func layoutThinking(b *Block, width int, _ int) []ScreenRow {
	prefix := kindStylePrefix(blockThinking)
	if b.meta.ThinkingRedacted {
		text := prefix + formatThinkingRedactedCollapsed() + reset
		return []ScreenRow{{Text: text, Tag: RowTag{BlockID: b.id, RowIdx: 0}}}
	}
	if b.meta.Collapsed {
		dur := b.meta.DurationMs
		if b.meta.Streaming && !b.meta.StartedAt.IsZero() {
			dur = time.Since(b.meta.StartedAt).Milliseconds()
		}
		text := prefix + formatThinkingCollapsed(len([]rune(b.raw)), dur, b.meta.TokensPerSec) + reset
		return []ScreenRow{{Text: text, Tag: RowTag{BlockID: b.id, RowIdx: 0}}}
	}
	var chunks []string
	if b.meta.Streaming {
		chunks = append(chunks, prefix+formatThinkingStreaming()+reset)
	}
	if b.raw != "" {
		// Apply dim+italic to every line of thinking text, not just the first.
		md := layoutRichMarkdown(b.raw, width, "")
		for _, ln := range md {
			chunks = append(chunks, prefix+ln+reset)
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

// finalizeThinking marks all in-flight thinking blocks complete and collapsed.
func (s *scrollback) finalizeThinking() {
	for i := range s.blocks {
		b := &s.blocks[i]
		if b.kind != blockThinking || !b.meta.Streaming {
			continue
		}
		b.meta.Streaming = false
		b.meta.Collapsed = true
		if !b.meta.StartedAt.IsZero() {
			b.meta.DurationMs = time.Since(b.meta.StartedAt).Milliseconds()
		}
		b.invalidateLayout()
	}
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

// toggleLastThinking toggles collapse on the most recent thinking block.
// Returns (toggled, nowCollapsed): toggled is false if there was no eligible
// block (or it was redacted); nowCollapsed is the resulting collapsed state.
func (s *scrollback) toggleLastThinking() (bool, bool) {
	for i := len(s.blocks) - 1; i >= 0; i-- {
		b := &s.blocks[i]
		if b.kind != blockThinking {
			continue
		}
		if b.meta.ThinkingRedacted {
			return false, true
		}
		b.meta.Collapsed = !b.meta.Collapsed
		b.invalidateLayout()
		return true, b.meta.Collapsed
	}
	return false, false
}

// appendThinkingRedacted adds a collapsed redacted-thinking placeholder block.
func (s *scrollback) appendThinkingRedacted() {
	b := s.newBlock(blockThinking, "")
	b.meta.ThinkingRedacted = true
	b.meta.Collapsed = true
	b.meta.Streaming = false
	s.blocks = append(s.blocks, b)
	s.totalAdded++
	s.trim()
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
