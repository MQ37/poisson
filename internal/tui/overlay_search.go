package tui

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// searchOverlay is an in-scrollback find bar (Ctrl+F).
type searchOverlay struct {
	query   string
	matches []int
	cur     int
	rows    func() []ScreenRow
	scroll  func(globalRow int)
}

func newSearchOverlay(rows func() []ScreenRow, scroll func(int)) *searchOverlay {
	return &searchOverlay{rows: rows, scroll: scroll}
}

func (s *searchOverlay) recompute() {
	s.matches = nil
	s.cur = 0
	q := strings.ToLower(strings.TrimSpace(s.query))
	if q == "" {
		return
	}
	for i, row := range s.rows() {
		if strings.Contains(strings.ToLower(stripANSI(row.Text)), q) {
			s.matches = append(s.matches, i)
		}
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
	s.recompute()
	q := s.query
	if q == "" {
		q = "_"
	}
	inner := cols - 6
	if inner < 20 {
		inner = cols - 4
	}
	label := fgYellow + bold + " search: " + reset + fgCyan + q + reset
	count := ""
	if s.query != "" {
		if len(s.matches) == 0 {
			count = dim + "  (no matches)" + reset
		} else {
			count = dim + "  " + itoa(len(s.matches)) + " · n/N · Esc" + reset
		}
	} else {
		count = dim + "  type to find · Esc close" + reset
	}
	line := truncateToWidth(label+count, cols)
	return 1, []string{line}
}

func (s *searchOverlay) feedKey(data []byte) (handled bool, done bool, cancel bool) {
	if isArrowUp(data) || isArrowDown(data) {
		return true, false, false
	}
	for _, b := range data {
		if b == 27 && !hasCSI(data) {
			return true, true, true
		}
	}
	if indexOf(data, []byte{'n'}) >= 0 && !hasCtrl(data) {
		s.next(1)
		return true, false, false
	}
	if indexOf(data, []byte{'N'}) >= 0 {
		s.next(-1)
		return true, false, false
	}
	for _, b := range data {
		if b == 127 || b == 8 {
			if len(s.query) > 0 {
				_, size := utf8.DecodeLastRuneInString(s.query)
				s.query = s.query[:len(s.query)-size]
				s.recompute()
			}
			return true, false, false
		}
	}
	for _, r := range string(data) {
		if r == '\r' || r == '\n' || r == '\t' {
			continue
		}
		if unicode.IsPrint(r) {
			s.query += string(r)
			s.recompute()
			return true, false, false
		}
	}
	return true, false, false
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

func hasCtrl(data []byte) bool {
	for _, b := range data {
		if b < 32 && b != 9 && b != 10 && b != 13 {
			return true
		}
	}
	return false
}

