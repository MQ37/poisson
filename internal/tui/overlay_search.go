package tui

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// searchOverlay is an in-scrollback find bar (Ctrl+F).
type searchOverlay struct {
	query     string
	lastQuery string
	matches   []int
	cur       int
	rows      func() []ScreenRow
	scroll    func(globalRow int)
	running   func() bool
}

func newSearchOverlay(rows func() []ScreenRow, scroll func(int), running func() bool) *searchOverlay {
	return &searchOverlay{rows: rows, scroll: scroll, running: running}
}

func (s *searchOverlay) updateMatches(resetCur bool) {
	q := strings.ToLower(strings.TrimSpace(s.query))
	if q == "" {
		s.matches = nil
		if resetCur || s.lastQuery != "" {
			s.cur = 0
		}
		s.lastQuery = ""
		return
	}
	var matches []int
	for i, row := range s.rows() {
		if strings.Contains(strings.ToLower(stripANSI(row.Text)), q) {
			matches = append(matches, i)
		}
	}
	if resetCur || q != s.lastQuery {
		s.cur = 0
	}
	s.lastQuery = q
	s.matches = matches
	if len(s.matches) > 0 && s.cur >= len(s.matches) {
		s.cur = len(s.matches) - 1
	}
	if resetCur && len(s.matches) > 0 && s.scroll != nil {
		s.scroll(s.matches[s.cur])
	}
}

func (s *searchOverlay) currentGlobalRow() int {
	if s.cur < 0 || s.cur >= len(s.matches) {
		return -1
	}
	return s.matches[s.cur]
}

func (s *searchOverlay) matchRows() []int {
	return append([]int(nil), s.matches...)
}

func (s *searchOverlay) render(scrollRows, cols int) (int, []string) {
	q := s.query
	if q == "" {
		q = dim + "type to find" + reset
	} else {
		q = fgCyan + q + reset
	}
	label := fgYellow + bold + " search: " + reset + q
	count := ""
	if s.query != "" {
		if len(s.matches) == 0 {
			count = dim + "  (no matches)" + reset
		} else {
			count = dim + "  " + itoa(s.cur+1) + "/" + itoa(len(s.matches)) + " · ↑↓ · Enter jump · Esc" + reset
		}
	} else {
		hint := " · Esc close · Ctrl+C dismiss"
		if s.running != nil && s.running() {
			hint += " · agent running"
		}
		count = dim + hint + reset
	}
	line := truncateToWidth(label+count, cols)
	return 1, []string{line}
}

func (s *searchOverlay) feedKey(k Key) (handled bool, done bool, cancel bool) {
	if k.isCtrlC() {
		return true, true, true
	}
	switch k.Kind {
	case KeyArrowUp, KeyShiftArrowUp:
		s.next(-1)
		return true, false, false
	case KeyArrowDown, KeyShiftArrowDown:
		s.next(1)
		return true, false, false
	case KeyEnter:
		if len(s.matches) > 0 && s.scroll != nil {
			s.scroll(s.matches[s.cur])
		}
		return true, false, false
	case KeyEscape:
		return true, true, true
	case KeyBackspace:
		if len(s.query) > 0 {
			_, size := utf8.DecodeLastRuneInString(s.query)
			s.query = s.query[:len(s.query)-size]
			s.updateMatches(true)
		}
		return true, false, false
	case KeyRune:
		if unicode.IsPrint(k.Rune) {
			s.query += string(k.Rune)
			s.updateMatches(true)
		}
		return true, false, false
	case KeyPaste:
		s.appendPaste(k.Text)
		return true, false, false
	}
	return false, false, false
}

func (s *searchOverlay) appendPaste(text string) bool {
	changed := false
	for _, r := range text {
		if unicode.IsPrint(r) {
			s.query += string(r)
			changed = true
		}
	}
	if changed {
		s.updateMatches(true)
	}
	return changed
}

// highlightSearchMatch wraps every case-insensitive query hit while preserving
// inline ANSI styling (bash highlights, markdown colors, etc.).
func highlightSearchMatch(line, query, pre, post string) string {
	if query == "" {
		return line
	}
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return line
	}
	plain := stripANSI(line)
	lower := strings.ToLower(plain)
	var spans [][2]int
	pos := 0
	for {
		idx := strings.Index(lower[pos:], q)
		if idx < 0 {
			break
		}
		idx += pos
		spans = append(spans, [2]int{idx, idx + len(q)})
		pos = idx + len(q)
	}
	if len(spans) == 0 {
		return line
	}
	return highlightPlainSpans(line, spans, pre, post)
}

func highlightPlainSpans(line string, spans [][2]int, pre, post string) string {
	var b strings.Builder
	spanIdx := 0
	plainIdx := 0
	inSpan := false

	for i := 0; i < len(line); {
		if !inSpan && spanIdx < len(spans) && plainIdx == spans[spanIdx][0] {
			b.WriteString(pre)
			inSpan = true
		}

		if line[i] == 0x1b && i+1 < len(line) {
			j := i + 2
			for j < len(line) {
				c := line[j]
				if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
					j++
					break
				}
				j++
			}
			b.WriteString(line[i:j])
			i = j
			continue
		}

		_, size := utf8.DecodeRuneInString(line[i:])
		if size <= 0 {
			size = 1
		}
		b.WriteString(line[i : i+size])
		plainIdx += 1
		i += size

		if inSpan && spanIdx < len(spans) && plainIdx == spans[spanIdx][1] {
			b.WriteString(post)
			inSpan = false
			spanIdx++
		}
	}

	if inSpan {
		b.WriteString(post)
	}
	b.WriteString(reset)
	return b.String()
}

func (s *searchOverlay) next(dir int) {
	if len(s.matches) == 0 {
		return
	}
	s.cur += dir
	if s.cur < 0 {
		s.cur = len(s.matches) - 1
	}
	if s.cur >= len(s.matches) {
		s.cur = 0
	}
	if s.scroll != nil {
		s.scroll(s.matches[s.cur])
	}
}