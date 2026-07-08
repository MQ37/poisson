package tui

import (
	"strings"
	"testing"
)

func TestBlockLayoutCache(t *testing.T) {
	b := Block{id: 1, kind: blockAssistant, raw: "hello world"}
	_ = b.layoutPlain(40)
	if b.cachedRows == nil {
		t.Fatal("expected cache populated")
	}
	cachedLen := len(b.cachedRows)
	_ = b.layoutPlain(40)
	if len(b.cachedRows) != cachedLen {
		t.Fatal("expected cache hit on second layout")
	}
	if b.cacheWidth != 40 {
		t.Fatalf("cacheWidth = %d", b.cacheWidth)
	}
	b.raw = "changed"
	b.invalidateLayout()
	if b.cachedRows != nil {
		t.Fatal("cache should be cleared after invalidate")
	}
}

// TestBlockLayoutStylesEveryWrappedLine guards against a real bug: a
// multi-line user message only had its style prefix on the FIRST wrapped
// row, so a continuation line rendered unstyled whenever it was repainted
// without the first row in the same batch (dirty-row repaints position the
// cursor per row — they don't replay a full top-to-bottom stream, so color
// state can't be assumed to "carry over" from a row outside the repaint).
func TestBlockLayoutStylesEveryWrappedLine(t *testing.T) {
	long := strings.Repeat("word ", 40) // wraps across several rows at width 20
	b := Block{id: 1, kind: blockUser, raw: long}
	rows := b.layoutPlain(20)
	if len(rows) < 2 {
		t.Fatalf("expected multiple wrapped rows, got %d", len(rows))
	}
	prefix := kindStylePrefix(blockUser)
	for i, row := range rows {
		if !strings.HasPrefix(row.Text, prefix) {
			t.Errorf("row %d missing style prefix: %q", i, row.Text)
		}
	}
}

func TestBlockMergeStreaming(t *testing.T) {
	s := newScrollback(1024)
	s.appendBlock(blockAssistant, "aa")
	s.appendBlock(blockAssistant, "bb")
	if s.blockCount() != 1 {
		t.Fatalf("blocks = %d, want 1", s.blockCount())
	}
	if s.blockRaw(0) != "aabb" {
		t.Fatalf("raw = %q", s.blockRaw(0))
	}
}

func TestBlockLayoutInvalidatesOnWidthChange(t *testing.T) {
	b := Block{id: 2, kind: blockAssistant, raw: strings.Repeat("x", 50)}
	w40 := b.layoutPlain(40)
	b.invalidateLayout()
	w20 := b.layoutPlain(20)
	if len(w20) <= len(w40) {
		t.Fatalf("narrower width should produce more rows: %d vs %d", len(w20), len(w40))
	}
	for _, row := range w20 {
		if row.Tag.BlockID != 2 {
			t.Fatalf("tag block id = %d", row.Tag.BlockID)
		}
	}
}

func TestScreenRowTagsUniquePerBlock(t *testing.T) {
	s := newScrollback(1024)
	s.append(StyledLine{Style: styleAssistant, Text: "line one"})
	s.append(StyledLine{Style: styleSystem, Text: "line two"})
	rows := s.visible(10, 20)
	if len(rows) < 2 {
		t.Fatalf("rows = %d", len(rows))
	}
	if rows[0].Tag.BlockID == rows[len(rows)-1].Tag.BlockID {
		t.Fatal("different blocks should have different tags")
	}
}