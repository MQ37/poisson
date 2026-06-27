package tui

import (
	"testing"
)

func TestSearchOverlayPreservesMatchIndexOnRender(t *testing.T) {
	rows := []ScreenRow{
		{Text: "alpha line"},
		{Text: "beta line"},
		{Text: "gamma line"},
	}
	s := newSearchOverlay(func() []ScreenRow { return rows }, nil)
	s.query = "line"
	s.updateMatches(true)
	s.cur = 2
	s.render(10, 80)
	if s.cur != 2 {
		t.Fatalf("render reset cur to %d, want 2", s.cur)
	}
}