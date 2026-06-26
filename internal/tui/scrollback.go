package tui

import (
	"strings"
	"unicode/utf8"
)

// LineStyle categorizes a scrollback line so we can color-code it cheaply
// without reparsing the text each render.
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

// StyledLine is one logical row in the scrollback. Text is the visible text
// WITHOUT ANSI codes; style is applied at render time.
type StyledLine struct {
	Style LineStyle
	Text  string // visible text only (no ANSI)
}

// scrollback is an append-only ring of styled logical lines. Logical lines are
// wrapped to the terminal width lazily in visible(). This keeps streamed
// assistant text on a single logical line instead of creating one scrollback
// row per SSE chunk.
type scrollback struct {
	lines    []StyledLine // logical, unwrapped lines
	maxLines int
	// viewport: scrollTop == 0 means "pinned to bottom" (live mode). Positive
	// values mean "user scrolled up by N rows".
	scrollTop  int
	totalAdded int // ever-appended counter (for status display)
}

func newScrollback(max int) *scrollback {
	if max < 1024 {
		max = 1024
	}
	return &scrollback{lines: make([]StyledLine, 0, 256), maxLines: max}
}

// streamingStyles are the line styles that should merge with the previous
// line when appended consecutively. Assistant text and thinking are streamed
// one SSE chunk at a time; without merging each chunk would appear on its own
// scrollback row.
var streamingStyles = map[LineStyle]bool{
	styleAssistant: true,
	styleThinking:  true,
}

// append adds a styled logical line. Consecutive streaming lines of the same
// style have their text merged into a single logical line.
// append adds styled text to the scrollback. The text may contain newlines;
// each line becomes its own logical scrollback row. For streaming styles
// (assistant text, thinking), the first fragment is merged into the previous
// row so a multi-chunk stream stays on one logical line until a real newline.
func (s *scrollback) append(line StyledLine) {
	text := stripANSI(line.Text)
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if text == "" && streamingStyles[line.Style] {
		return
	}
	parts := strings.Split(text, "\n")
	start := 0
	if streamingStyles[line.Style] && len(s.lines) > 0 && s.lines[len(s.lines)-1].Style == line.Style {
		s.lines[len(s.lines)-1].Text += sanitizeControls(parts[0])
		start = 1
	}
	for i := start; i < len(parts); i++ {
		s.lines = append(s.lines, StyledLine{Style: line.Style, Text: sanitizeControls(parts[i])})
	}
	s.totalAdded++
	s.trim()
}

// appendRaw appends pre-split text (multiple lines already separated by \r\n
// or \n). Used for /commands and tool-result output. Long logical lines are
// wrapped at render time.
func (s *scrollback) appendRaw(style LineStyle, text string) {
	for _, ln := range splitLines(stripANSI(text)) {
		s.lines = append(s.lines, StyledLine{Style: style, Text: sanitizeControls(ln)})
		s.totalAdded++
	}
	s.trim()
}

// sanitizeControls makes one logical line safe to render: tabs expand to
// spaces and other C0 control bytes (which would move the terminal cursor)
// are dropped. The caller must have already split on '\n'.
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
			// drop other C0 controls / DEL
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (s *scrollback) trim() {
	if len(s.lines) > s.maxLines {
		drop := len(s.lines) - s.maxLines
		s.lines = s.lines[drop:]
		// If user had scrolled up, the floor shifts: clamp.
		if s.scrollTop > 0 {
			s.scrollTop -= drop
			if s.scrollTop < 0 {
				s.scrollTop = 0
			}
		}
	}
	if s.scrollTop > len(s.lines) {
		s.scrollTop = len(s.lines)
	}
}

// visible returns the slice of screen rows currently in the viewport. It wraps
// logical lines to width and honors scrollTop so the user can scroll back.
func (s *scrollback) visible(height, width int) []StyledLine {
	if height < 1 || width < 1 || len(s.lines) == 0 {
		return nil
	}
	// Wrap all logical lines to width, counting screen rows.
	wrapped, cumulative := wrapAll(s.lines, width)
	if len(wrapped) == 0 {
		return nil
	}
	end := len(wrapped)
	start := end - height
	if start < 0 {
		start = 0
	}
	if s.scrollTop > 0 {
		// scrollTop is logical-line rows. Convert to wrapped rows by finding
		// the wrapped row index that corresponds to scrolling up that many
		// logical lines from the bottom.
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
	return wrapped[start:end]
}

// wrapAll wraps every logical line to width and returns the flat list of
// screen rows. cumulative[i] is the number of wrapped rows consumed by lines
// [0:i), so the caller can map a logical-line scroll offset to a wrapped row.
func wrapAll(lines []StyledLine, width int) ([]StyledLine, []int) {
	cumulative := make([]int, len(lines)+1)
	var out []StyledLine
	for i, ln := range lines {
		chunks := wrapLine(ln.Text, width)
		prefix := stylePrefix(ln.Style)
		for _, chunk := range chunks {
			out = append(out, StyledLine{Style: ln.Style, Text: prefix + chunk + reset})
		}
		cumulative[i+1] = cumulative[i] + len(chunks)
	}
	return out, cumulative
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

func (s *scrollback) pinned() bool { return s.scrollTop == 0 }

func (s *scrollback) scrollUp(n, height int) {
	// scrollTop counts logical lines. We can't scroll beyond the top.
	if s.scrollTop+n > len(s.lines) {
		s.scrollTop = len(s.lines)
	} else {
		s.scrollTop += n
	}
}

func (s *scrollback) scrollDown(n int) {
	s.scrollTop -= n
	if s.scrollTop < 0 {
		s.scrollTop = 0
	}
}

func (s *scrollback) scrollToBottom() { s.scrollTop = 0 }

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

// splitLines splits on \r\n or \n. Empty lines preserved.
func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// stripANSI removes ANSI escape sequences for width measurement and storage.
func stripANSI(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == 0x1b {
			// Skip ESC + [ + params (digits, semicolons) + final byte (0x40-0x7e).
			i++
			if i < len(s) && s[i] == '[' {
				i++
				for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
					i++
				}
				if i < len(s) {
					i++ // consume final byte
				}
				continue
			}
			if i < len(s) && s[i] == ']' { // OSC: ESC ] ... BEL or ESC \
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
			// Other ESC: skip 1 byte.
			i++
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// visibleWidth returns the display width of a string in runes (no CJK width).
func visibleWidth(s string) int { return utf8.RuneCountInString(stripANSI(s)) }

// truncateToWidth truncates a (possibly ANSI-bearing) string to fit in
// `width` visible columns, appending "…" if cut. Used by status bar.
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
