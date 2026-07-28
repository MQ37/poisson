package tui

import (
	"strings"
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
	blocks        []Block
	maxLines      int // max logical blocks (name kept for compat)
	scrollOffset  int // screen rows scrolled up from bottom; 0 = live tail
	totalAdded    int
	nextID        int64
	focusedToolID int64 // expanded tool card receiving ↑↓ scroll

	// lastWidth/lastRowCount cache the wrapped row count as of the last time
	// clampScrollOffset ran (every paint), so compensateGrowth can tell freshly
	// streamed-in tail growth apart from a width change (resize re-wraps
	// everything and legitimately changes the count for unrelated reasons).
	lastWidth    int
	lastRowCount int

	// pendingSpeedBlocks holds the IDs of every block created or extended by
	// the round currently in flight (thinking, assistant text, tool calls) —
	// applyInferenceSpeed tags all of them with the round's tok/s once the
	// agent reports it (see agent.OutputInferenceSpeed) and then clears this.
	pendingSpeedBlocks map[int64]bool

	// Session-wide weighted average output tokens/sec. Each completed round
	// with a real reading contributes (tokens, tokens/sec) and the header
	// shows the running average. Zero until the first measurable round.
	speedTokenSum float64 // Σ output_tokens across measured rounds
	speedRateSum  float64 // Σ (tokens_per_sec * output_tokens) — for weighted avg
}

// markRoundBlock records id as belonging to the round currently in flight, so
// a later applyInferenceSpeed call knows to tag it. Safe to call repeatedly
// for the same id (e.g. once per streamed chunk merged into one tail block).
func (s *scrollback) markRoundBlock(id int64) {
	if s.pendingSpeedBlocks == nil {
		s.pendingSpeedBlocks = make(map[int64]bool, 4)
	}
	s.pendingSpeedBlocks[id] = true
}

// applyInferenceSpeed tags every block the in-flight round produced with its
// average output tokens/sec, then clears the pending set — see
// agent.OutputInferenceSpeed for why one figure applies to the whole round.
// When outputTokens > 0 and tokPerSec > 0 the session-wide weighted average
// is updated so the header can show a running "N tok/s" for the conversation.
func (s *scrollback) applyInferenceSpeed(tokPerSec float64, outputTokens int) {
	if len(s.pendingSpeedBlocks) == 0 {
		return
	}
	for i := range s.blocks {
		if s.pendingSpeedBlocks[s.blocks[i].id] {
			s.blocks[i].meta.TokensPerSec = tokPerSec
			s.blocks[i].invalidateLayout()
		}
	}
	s.pendingSpeedBlocks = nil
	if tokPerSec > 0 && outputTokens > 0 {
		w := float64(outputTokens)
		s.speedTokenSum += w
		s.speedRateSum += tokPerSec * w
	}
}

// avgTokensPerSec is the session-wide weighted average output tokens/sec
// (0 when no measurable round has completed yet).
func (s *scrollback) avgTokensPerSec() float64 {
	if s.speedTokenSum <= 0 {
		return 0
	}
	return s.speedRateSum / s.speedTokenSum
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
		if kind == blockThinking {
			if len(s.blocks) > 0 && s.blocks[len(s.blocks)-1].kind == blockThinking {
				s.markThinkingStreaming()
				s.markRoundBlock(s.blocks[len(s.blocks)-1].id)
				return
			}
			b := s.newBlock(kind, "")
			b.meta.Streaming = true
			markStarted(&b.meta)
			s.blocks = append(s.blocks, b)
			s.markThinkingStreaming()
			s.markRoundBlock(b.id)
			s.totalAdded++
			s.trim()
		}
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
				s.markRoundBlock(tail.id)
				s.totalAdded++
				s.trim()
				return
			}
		}
		b := s.newBlock(kind, sanitizeControls(text))
		if kind == blockThinking {
			b.meta.Streaming = true
			markStarted(&b.meta)
		}
		s.blocks = append(s.blocks, b)
		s.markRoundBlock(b.id)
		if kind == blockThinking {
			s.markThinkingStreaming()
		}
		s.totalAdded++
		s.trim()
		return
	}
	if kind == blockUser {
		// A submitted message is one semantic turn however many lines it spans.
		// Splitting per line like the fallback below would make
		// userBlockIndices/stepConvPrompt (Shift+Left/Right conversation-turn
		// navigation) treat each line of one multi-line message as its own
		// separate turn. wrapLine hard-wraps on \n correctly, so storing the
		// whole raw multi-line text in one block renders fine.
		s.blocks = append(s.blocks, s.newBlock(kind, sanitizeControls(text)))
		s.totalAdded++
		s.trim()
		return
	}
	for _, part := range strings.Split(text, "\n") {
		s.blocks = append(s.blocks, s.newBlock(kind, sanitizeControls(part)))
	}
	s.totalAdded++
	s.trim()
}

func (s *scrollback) append(line StyledLine) {
	s.appendBlock(styleToKind(line.Style), line.Text)
}

func (s *scrollback) prependIntroLines(lines []string) {
	s.stripIntroBlocks()
	if len(lines) == 0 {
		return
	}
	newBlocks := make([]Block, 0, len(lines))
	for _, ln := range lines {
		newBlocks = append(newBlocks, s.newBlock(blockIntro, ln))
	}
	s.blocks = append(newBlocks, s.blocks...)
	s.totalAdded += len(lines)
	s.trim()
}

func (s *scrollback) stripIntroBlocks() {
	if len(s.blocks) == 0 {
		return
	}
	out := s.blocks[:0]
	for _, b := range s.blocks {
		if b.kind != blockIntro {
			out = append(out, b)
		}
	}
	s.blocks = out
}

func (s *scrollback) appendRaw(style LineStyle, text string) {
	for _, ln := range splitLines(stripANSI(text)) {
		s.blocks = append(s.blocks, s.newBlock(styleToKind(style), sanitizeControls(ln)))
		s.totalAdded++
	}
	s.trim()
}

// streamViewportDirty returns scroll-region row indices to repaint after a
// streaming append. When pinned to the bottom (scrollOffset == 0), the entire
// visible viewport is repainted so tail growth that shifts viewStart upward
// (chat taller than the window) does not leave stale rows above a lone live
// last line. Returns nil when the user has scrolled up.
func (s *scrollback) streamViewportDirty(height, width int) []int {
	if height < 1 || width < 1 || len(s.blocks) == 0 || s.scrollOffset > 0 {
		return nil
	}
	wrapped, _ := s.layoutAll(width)
	viewEnd := len(wrapped) - s.scrollOffset
	if viewEnd < 1 {
		return nil
	}
	viewStart := viewEnd - height
	if viewStart < 0 {
		viewStart = 0
	}
	viewLen := viewEnd - viewStart
	if viewLen < 1 {
		return nil
	}
	rows := make([]int, viewLen)
	for i := range rows {
		rows[i] = i
	}
	return rows
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
		case r == '\n' || r == '\r':
			// Must survive control-char stripping: appendBlock already normalized
			// \r\n/\r to \n upstream specifically so line breaks and markdown fence
			// structure stay intact (its own doc comment says so). r < 0x20 below
			// would otherwise also match \n (0x0A) and silently drop it — harmless
			// while no OTHER control char is present (the ContainsAny fast path
			// above bails out early and returns s unchanged), but as soon as a
			// chunk also contains e.g. a literal tab (common in code blocks the
			// model indents with \t), this loop ran and ate every newline in the
			// whole chunk, not just the tab — collapsing multi-paragraph markdown
			// into one flat, unstructured line.
			b.WriteRune(r)
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
		for _, b := range s.blocks[:drop] {
			if b.id == s.focusedToolID {
				s.focusedToolID = 0
				break
			}
		}
		s.blocks = s.blocks[drop:]
	}
}

// layoutAll lays out every block at width and returns flat screen rows plus
// cumulative[i] = wrapped row count for blocks[0:i).
func (s *scrollback) layoutAll(width int) ([]ScreenRow, []int) {
	cumulative := make([]int, len(s.blocks)+1)
	var out []ScreenRow
	for i := range s.blocks {
		// Running subagent widgets are ALSO pinned above the conversation (a
		// glanceable status the user won't miss while scrolled elsewhere), but
		// they render inline here too, at their actual point in the
		// conversation — exactly like any other tool call — so scrolling back
		// through history shows where each subagent was spawned.
		rows := s.blocks[i].layoutPlain(width)
		out = append(out, rows...)
		cumulative[i+1] = len(out)
	}
	return out, cumulative
}

// compensateGrowth keeps the viewport pinned to the same absolute content
// while the user is scrolled up and new rows stream in at the tail.
// scrollOffset means "rows from the bottom", but the bottom keeps moving as
// the assistant streams — left alone, that silently drags a scrolled-up
// view down to a new slice of content on every single streamed line, even
// though the user never touched a scroll key. Only applied when width
// matches the last check: a resize re-wraps everything and legitimately
// changes the row count for unrelated reasons, and the clamp below already
// keeps a stale offset in range in that case.
func (s *scrollback) compensateGrowth(width, rowCount int) {
	if s.scrollOffset > 0 && width == s.lastWidth && rowCount > s.lastRowCount {
		s.scrollOffset += rowCount - s.lastRowCount
	}
	s.lastWidth = width
	s.lastRowCount = rowCount
}

func (s *scrollback) clampScrollOffset(height, width int) {
	if height < 1 {
		height = 1
	}
	wrapped, _ := s.layoutAll(width)
	s.compensateGrowth(width, len(wrapped))
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

// viewportStart returns the absolute index (into layoutAll's row list) of the
// first row currently on screen. Combined with a viewport-relative row, this
// gives a stable address for mouse-driven text selection that survives
// scrolling (layoutAll is stable for a given width).
func (s *scrollback) viewportStart(height, width int) int {
	_, start, _ := s.viewportRange(height, width)
	return start
}

// selectedText extracts the plain (ANSI-stripped) text spanning absolute rows
// [loRow,hiRow] at the given width, clipped to [loCol,hiCol] on the first/last
// row. Returns "" if the range is out of bounds.
func (s *scrollback) selectedText(width, loRow, loCol, hiRow, hiCol int) string {
	wrapped, _ := s.layoutAll(width)
	if len(wrapped) == 0 || loRow >= len(wrapped) {
		return ""
	}
	if hiRow >= len(wrapped) {
		hiRow = len(wrapped) - 1
	}
	var lines []string
	for r := loRow; r <= hiRow; r++ {
		runes := []rune(stripANSI(wrapped[r].Text))
		start, end := 0, len(runes)
		if r == loRow {
			start = clampInt(loCol, 0, len(runes))
		}
		if r == hiRow {
			end = clampInt(hiCol+1, 0, len(runes))
		}
		if start > end {
			start = end
		}
		lines = append(lines, strings.TrimRight(string(runes[start:end]), " "))
	}
	return strings.Join(lines, "\n")
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
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

func (s *scrollback) scrollToTop(height, width int) {
	if height < 1 {
		height = 1
	}
	if width < 1 {
		width = 1
	}
	wrapped, _ := s.layoutAll(width)
	max := len(wrapped) - height
	if max < 0 {
		max = 0
	}
	s.scrollOffset = max
}

// blockCount returns the number of logical blocks (for tests).
func (s *scrollback) blockCount() int { return len(s.blocks) }

// blockRaw returns the raw text of block i (for tests).
func (s *scrollback) blockRaw(i int) string {
	if i < 0 || i >= len(s.blocks) {
		return ""
	}
	return s.blocks[i].raw
}

// appendToolCallReplay adds a completed tool card during session hydrate (no live timer).
func (s *scrollback) appendToolCallReplay(id int64, providerCallID, name string, input []byte) {
	b := s.newBlock(blockToolCall, "")
	b.meta = BlockMeta{
		ToolName:       name,
		ProviderCallID: providerCallID,
		ToolInput:      append([]byte(nil), input...),
		Streaming:      false,
		// edit/write always fully visible on resume too.
		Expanded: isDiffTool(name),
	}
	s.blocks = append(s.blocks, b)
	s.totalAdded++
	s.trim()
}

// wrapLine wraps text to width runes per chunk (word-aware when spaces are
// present; hard wrap for long tokens). Despite historically being named for
// "a single logical line", callers commonly pass raw multi-paragraph text
// (a user's typed message, a tool's multi-line result) — embedded \n must
// start a new wrapped line, not get left as a literal byte inside one: this
// renderer positions the cursor per row and writes raw bytes, so an
// unexpected \n moves the cursor instead of being a soft line break,
// corrupting the display.
func wrapLine(text string, width int) []string {
	if !strings.Contains(text, "\n") {
		return wrapWords(text, width)
	}
	var out []string
	for _, para := range strings.Split(text, "\n") {
		out = append(out, wrapWords(para, width)...)
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
	if visibleWidth(s) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	var b strings.Builder
	vis := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) {
			j := i + 2
			for j < len(s) {
				c := s[j]
				if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
					j++
					break
				}
				j++
			}
			b.WriteString(s[i:j])
			i = j
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		if vis >= width-1 {
			b.WriteString("…")
			return b.String()
		}
		b.WriteString(s[i : i+size])
		vis++
		i += size
	}
	return b.String()
}
