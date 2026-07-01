package tui

import (
	"strings"
	"testing"
	"time"
)

func TestThinkingCollapsedHeader(t *testing.T) {
	b := Block{
		id:   1,
		kind: blockThinking,
		raw:  "secret reasoning",
		meta: BlockMeta{Collapsed: true, DurationMs: 2300},
	}
	rows := layoutThinking(&b, 60, 0)
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	plain := stripANSI(rows[0].Text)
	if !strings.Contains(plain, "▸ thinking") || !strings.Contains(plain, "16 chars") {
		t.Fatalf("got %q", plain)
	}
}

func TestThinkingFinalizeCollapses(t *testing.T) {
	s := newScrollback(1024)
	s.appendBlock(blockThinking, "hmm")
	s.finalizeThinking()
	b := s.blocks[0]
	if !b.meta.Collapsed || b.meta.Streaming {
		t.Fatalf("meta = %+v", b.meta)
	}
}

func TestThinkingFinalizeMultilineSingleBlock(t *testing.T) {
	s := newScrollback(1024)
	s.appendBlock(blockThinking, "line1\nline2")
	s.finalizeThinking()
	if s.blockCount() != 1 {
		t.Fatalf("blocks = %d", s.blockCount())
	}
	if s.blocks[0].meta.Streaming || !s.blocks[0].meta.Collapsed {
		t.Fatalf("meta = %+v", s.blocks[0].meta)
	}
}

func TestThinkingToggleLast(t *testing.T) {
	s := newScrollback(1024)
	s.appendBlock(blockThinking, "long thought")
	s.finalizeThinking()
	if !s.blocks[0].meta.Collapsed {
		t.Fatal("expected collapsed")
	}
	if !s.toggleLastThinking() {
		t.Fatal("toggle failed")
	}
	if s.blocks[0].meta.Collapsed {
		t.Fatal("expected expanded after toggle")
	}
}

func TestThinkingToggleLastWhileStreaming(t *testing.T) {
	s := newScrollback(1024)
	s.appendBlock(blockThinking, "still going")
	if !s.toggleLastThinking() {
		t.Fatal("toggle failed while streaming")
	}
	if !s.blocks[0].meta.Collapsed || !s.blocks[0].meta.Streaming {
		t.Fatalf("meta = %+v", s.blocks[0].meta)
	}
}

func TestThinkingRedactedCollapsed(t *testing.T) {
	b := Block{
		id:   3,
		kind: blockThinking,
		meta: BlockMeta{ThinkingRedacted: true, Collapsed: true},
	}
	rows := layoutThinking(&b, 40, 0)
	if len(rows) != 1 || !strings.Contains(stripANSI(rows[0].Text), "redacted") {
		t.Fatalf("got %q", rows[0].Text)
	}
}

func TestThinkingStreamingExpanded(t *testing.T) {
	b := Block{
		id:   2,
		kind: blockThinking,
		raw:  "still thinking",
		meta: BlockMeta{Streaming: true, StartedAt: time.Now()},
	}
	rows := layoutThinking(&b, 40, 0)
	if len(rows) < 2 {
		t.Fatalf("rows = %d", len(rows))
	}
	if !strings.Contains(stripANSI(rows[0].Text), "▾ thinking") {
		t.Fatalf("got %q", rows[0].Text)
	}
}