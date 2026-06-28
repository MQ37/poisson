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
}

func newSearchOverlay(rows func() []ScreenRow, scroll func(int)) *searchOverlay {
	return &searchOverlay{rows: rows, scroll: scroll}
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
	s.updateMatches(false)
	q := s.query
	if q == "" {
		q = dim + "type to find" + reset
	} else {
		q = fgCyan + q + reset
	}
	inner := cols - 6
	if inner < 20 {
		inner = cols - 4
	}
	label := fgYellow + bold + " search: " + reset + q
	count := ""
	if s.query != "" {
		if len(s.matches) == 0 {
			count = dim + "  (no matches)" + reset
		} else {
			count = dim + "  " + itoa(s.cur+1) + "/" + itoa(len(s.matches)) + " · ↑↓ · Esc" + reset
		}
	} else {
		count = dim + "  · Esc close · Ctrl+C dismiss" + reset
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
	return true, false, false
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

// highlightSearchMatch wraps the first case-insensitive query hit, preserving existing ANSI.
func highlightSearchMatch(line, query, pre, post string) string {
	if query == "" {
		return line
	}
	plain := stripANSI(line)
	lower := strings.ToLower(plain)
	q := strings.ToLower(strings.TrimSpace(query))
	idx := strings.Index(lower, q)
	if idx < 0 {
		return line
	}
	before := plain[:idx]
	match := plain[idx : idx+len(q)]
	after := plain[idx+len(q):]
	return pre + before + bold + match + post + after + reset
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