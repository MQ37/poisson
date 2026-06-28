package tui

import "testing"

func TestScrollBlockToTop(t *testing.T) {
	s := newScrollback(1024)
	s.append(StyledLine{Style: styleUser, Text: "first"})
	for i := 0; i < 30; i++ {
		s.append(StyledLine{Style: styleAssistant, Text: "filler line " + itoa(i)})
	}
	s.append(StyledLine{Style: styleUser, Text: "target prompt"})
	s.scrollToBottom()

	idxs := s.userBlockIndices()
	if len(idxs) != 2 {
		t.Fatalf("user blocks = %d", len(idxs))
	}
	s.scrollBlockToTop(idxs[0], 8, 40, 0)
	if s.scrollOffset == 0 {
		t.Fatal("expected scroll up to show first prompt")
	}
}