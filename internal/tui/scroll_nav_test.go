package tui

import (
	"strings"
	"testing"
)

func TestScrollWithinSingleBlock(t *testing.T) {
	s := newScrollback(1024)
	s.append(StyledLine{Style: styleAssistant, Text: strings.Repeat("word ", 200)})
	rows := s.visible(5, 20)
	if len(rows) != 5 {
		t.Fatalf("visible = %d rows", len(rows))
	}
	wrapped, _, _ := s.viewportRange(5, 20)
	if len(wrapped) < 10 {
		t.Fatalf("expected many wrapped rows, got %d", len(wrapped))
	}
	s.scrollUp(3)
	s.clampScrollOffset(5, 20)
	if s.scrollOffset != 3 {
		t.Fatalf("offset = %d", s.scrollOffset)
	}
	up := s.visible(5, 20)
	if len(up) != 5 {
		t.Fatalf("scrolled visible = %d", len(up))
	}
	if up[0].Tag.RowIdx == rows[0].Tag.RowIdx {
		t.Fatalf("scroll up should change viewport row index: %d vs %d", up[0].Tag.RowIdx, rows[0].Tag.RowIdx)
	}
}

func TestScrollDeltaForKeyPageKeys(t *testing.T) {
	if d, ok := scrollDeltaForKey(Key{Kind: KeyPageUp}, 10); !ok || d != 10 {
		t.Fatalf("PageUp = %d %v", d, ok)
	}
	if d, ok := scrollDeltaForKey(Key{Kind: KeyPageDown}, 10); !ok || d != -10 {
		t.Fatalf("PageDown = %d %v", d, ok)
	}
}

func TestDecoderKittyPageUpScrollDelta(t *testing.T) {
	var d Decoder
	keys := d.Push([]byte{27, '[', '5', '7', '3', '5', '4', 'u'})
	if len(keys) != 1 || keys[0].Kind != KeyPageUp {
		t.Fatalf("keys = %v", keys)
	}
	if delta, ok := scrollDeltaForKey(keys[0], 12); !ok || delta != 12 {
		t.Fatalf("delta=%d ok=%v", delta, ok)
	}
}

