package tui

import (
	"strings"
	"time"
	"unicode/utf8"
)

// LineStyle categorizes a scrollback line so we can color-code it cheaply
// without reparsing the text each render. Kept for commands.go compatibility.
type LineStyle uint8

const (
	styleUser       LineStyle = iota // user input
	styleAssistant                   // assistant text
	styleThinking                    // assistant thinking (dim)
	styleToolStart                   // tool invocation line
	styleToolResult                  // tool result line
	styleApproval                    // approval prompt
	styleError                       // error
	styleSystem                      // system (slash command output, /help)
	styleCompacting                  // compacting context
	styleStatus                      // status bar
)

// StyledLine is the legacy append unit; converted to Block internally.
type StyledLine struct {
	Style LineStyle
	Text  string
}

// scrollback is an append-only ring of document blocks. Blocks are laid out
// to screen rows lazily with per-block caching.
type scrollback struct {
	blocks   []Block
	maxLines int // max logical blocks (name kept for compat)
	scrollOffset int // screen rows scrolled up from bottom; 0 = live tail
	totalAdded int
	lastStreamWrapCount int
	nextID     int64
}

func newScrollback(max int) *scrollback {
	if max < 1024 {
		max = 1024
	}
	return &scrollback{blocks: make([]Block, 0, 256), maxLines: max, nextID: 1}
}

func (s *scrollback) newBlock(kind BlockKind, raw string) Block {
	id := s.nextID
	s.nextID++
	return Block{id: id, kind: kind, raw: raw}
}

// appendBlock adds or merges a block. Streaming kinds (assistant/thinking) merge
// the full chunk into the tail block of the same kind, preserving embedded
// newlines so markdown code fences stay intact across streamed chunks.
func (s *scrollback) appendBlock(kind BlockKind, raw string) {
	text := stripANSI(raw)
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if text == "" && streamingKinds[kind] {
		return
	}
	if streamingKinds[kind] {
		if len(s.blocks) > 0 {
			tail := &s.blocks[len(s.blocks)-1]
			if tail.kind == kind {
				tail.raw += sanitizeControls(text)
				tail.invalidateLayout()
				if kind == blockThinking {
					s.markThinkingStreaming()
				}
				s.totalAdded++
				s.trim()
				return
			}
		}
		s.lastStreamWrapCount = 0
		b := s.newBlock(kind, sanitizeControls(text))
		if kind == blockThinking {
			b.meta.Streaming = true
			b.meta.StartedAt = time.Now()
		}
		s.blocks = append(s.blocks, b)
		if kind == blockThinking {
			s.markThinkingStreaming()
		}
		s.totalAdded++
		s.trim()
		return
	}
	s.lastStreamWrapCount = 0
	for _, part := range strings.Split(text, "\n") {
		s.blocks = append(s.blocks, s.newBlock(kind, sanitizeControls(part)))
	}
	s.totalAdded++
	s.trim()
}

func (s *scrollback) append(line StyledLine) {
	s.appendBlock(styleToKind(line.Style), line.Text)
}

func (s *scrollback) appendRaw(style LineStyle, text string) {
	s.lastStreamWrapCount = 0
	for _, ln := range splitLines(stripANSI(text)) {
		s.blocks = append(s.blocks, s.newBlock(styleToKind(style), sanitizeControls(ln)))
		s.totalAdded++
	}
	s.trim()
}

func (s *scrollback) streamViewportDirty(height, width int) []int {
	if height < 1 || width < 1 || len(s.blocks) == 0 || s.scrollOffset > 0 {
		return nil
	}
	newCount := len(wrapLine(s.blocks[len(s.blocks)-1].raw, width))
	prev := s.lastStreamWrapCount
	grew := newCount > prev
	s.lastStreamWrapCount = newCount

	wrapped, _ := s.layoutAll(width)
	viewStart := len(wrapped) - height
	if viewStart < 0 {
		viewStart = 0
	}
	viewLen := len(wrapped) - viewStart
	if viewLen < 1 {
		return nil
	}
	if prev == 0 || grew {
		rows := make([]int, viewLen)
		for i := range rows {
			rows[i] = i
		}
		return rows
	}
	return []int{viewLen - 1}
}

func sanitizeControls(s string) string {
	if !strings.ContainsAny(s, "\t\x00\x01\x02\x03\x04\x05\x06\a\b\v\f\x0e\x0f\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f\x7f") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\t':
			b.WriteString("    ")
		case r < 0x20 || r == 0x7f:
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (s *scrollback) trim() {
	if len(s.blocks) > s.maxLines {
		drop := len(s.blocks) - s.maxLines
		s.blocks = s.blocks[drop:]
	}
}

// layoutAll lays out every block at width and returns flat screen rows plus
// cumulative[i] = wrapped row count for blocks[0:i).
func (s *scrollback) layoutAll(width int) ([]ScreenRow, []int) {
	cumulative := make([]int, len(s.blocks)+1)
	var out []ScreenRow
	for i := range s.blocks {
		rows := s.blocks[i].layoutPlain(width)
		out = append(out, rows...)
		cumulative[i+1] = len(out)
	}
	return out, cumulative
}

func (s *scrollback) clampScrollOffset(height, width int) {
	if height < 1 {
		height = 1
	}
	wrapped, _ := s.layoutAll(width)
	max := len(wrapped) - height
	if max < 0 {
		max = 0
	}
	if s.scrollOffset > max {
		s.scrollOffset = max
	}
	if s.scrollOffset < 0 {
		s.scrollOffset = 0
	}
}

// viewportRange returns laid-out rows and the [start,end) slice for the viewport.
func (s *scrollback) viewportRange(height, width int) ([]ScreenRow, int, int) {
	if height < 1 || width < 1 || len(s.blocks) == 0 {
		return nil, 0, 0
	}
	wrapped, _ := s.layoutAll(width)
	if len(wrapped) == 0 {
		return nil, 0, 0
	}
	s.clampScrollOffset(height, width)
	end := len(wrapped) - s.scrollOffset
	start := end - height
	if start < 0 {
		start = 0
	}
	if end > len(wrapped) {
		end = len(wrapped)
	}
	return wrapped, start, end
}

// visible returns screen rows in the current viewport.
func (s *scrollback) visible(height, width int) []ScreenRow {
	wrapped, start, end := s.viewportRange(height, width)
	if len(wrapped) == 0 {
		return nil
	}
	return wrapped[start:end]
}

func (s *scrollback) pinned() bool { return s.scrollOffset == 0 }

func (s *scrollback) scrollUp(n int) {
	if n < 1 {
		n = 1
	}
	s.scrollOffset += n
}

func (s *scrollback) scrollDown(n int) {
	if n < 1 {
		n = 1
	}
	s.scrollOffset -= n
	if s.scrollOffset < 0 {
		s.scrollOffset = 0
	}
}

func (s *scrollback) scrollToBottom() { s.scrollOffset = 0 }

// blockCount returns the number of logical blocks (for tests).
func (s *scrollback) blockCount() int { return len(s.blocks) }

// blockRaw returns the raw text of block i (for tests).
func (s *scrollback) blockRaw(i int) string {
	if i < 0 || i >= len(s.blocks) {
		return ""
	}
	return s.blocks[i].raw
}

// wrapLine hard-wraps a single logical line to width runes per chunk.
func wrapLine(text string, width int) []string {
	if width < 1 {
		width = 1
	}
	runes := []rune(text)
	if len(runes) == 0 {
		return []string{""}
	}
	if len(runes) <= width {
		return []string{text}
	}
	var out []string
	for len(runes) > width {
		out = append(out, string(runes[:width]))
		runes = runes[width:]
	}
	if len(runes) > 0 {
		out = append(out, string(runes))
	}
	return out
}

// stylePrefix returns the ANSI prefix for a given line style.
func stylePrefix(st LineStyle) string {
	switch st {
	case styleUser:
		return bgBlue + fgBlack + bold + " " + reset + " " + fgCyan + bold
	case styleAssistant:
		return fgGreen
	case styleThinking:
		return dim + italic
	case styleToolStart:
		return fgYellow
	case styleToolResult:
		return fgGray
	case styleApproval:
		return fgYellow + bold
	case styleError:
		return fgRed + bold
	case styleSystem:
		return fgMagenta
	case styleCompacting:
		return fgMagenta + italic
	case styleStatus:
		return dim
	}
	return ""
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func stripANSI(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == 0x1b {
			i++
			if i < len(s) && s[i] == '[' {
				i++
				for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
					i++
				}
				if i < len(s) {
					i++
				}
				continue
			}
			if i < len(s) && s[i] == ']' {
				i++
				for i < len(s) && s[i] != 0x07 {
					if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
						i += 2
						break
					}
					i++
				}
				if i < len(s) && s[i] == 0x07 {
					i++
				}
				continue
			}
			i++
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func visibleWidth(s string) int { return utf8.RuneCountInString(stripANSI(s)) }

func truncateToWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	plain := stripANSI(s)
	runes := []rune(plain)
	if len(runes) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	return string(runes[:width-1]) + "…"
}