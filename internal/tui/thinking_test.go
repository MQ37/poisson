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

func TestThinkingToggleInView(t *testing.T) {
	s := newScrollback(1024)
	s.appendBlock(blockThinking, "long thought")
	s.finalizeThinking()
	if !s.blocks[0].meta.Collapsed {
		t.Fatal("expected collapsed")
	}
	if !s.toggleThinkingInView(5, 40) {
		t.Fatal("toggle failed")
	}
	if s.blocks[0].meta.Collapsed {
		t.Fatal("expected expanded after toggle")
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