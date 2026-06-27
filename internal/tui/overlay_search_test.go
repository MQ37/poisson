package tui

import "testing"

func TestSearchOverlayFindsMatches(t *testing.T) {
	rows := []ScreenRow{
		{Text: "hello world"},
		{Text: "foo bar"},
		{Text: "hello again"},
	}
	o := newSearchOverlay(func() []ScreenRow { return rows }, nil)
	o.query = "hello"
	o.recompute()
	if len(o.matches) != 2 {
		t.Fatalf("matches = %v, want 2", o.matches)
	}
}

func TestSearchOverlayNext(t *testing.T) {
	var scrolled int
	o := newSearchOverlay(
		func() []ScreenRow {
			return []ScreenRow{{Text: "a"}, {Text: "b"}}
		},
		func(g int) { scrolled = g },
	)
	o.query = "a"
	o.recompute()
	o.next(1)
	if scrolled != o.matches[0] {
		t.Fatalf("scroll = %d, want %d", scrolled, o.matches[0])
	}
}