package tui

import (
	"encoding/json"
	"fmt"
)

const (
	toolResultCollapsedBytes = 400
	toolResultCollapsedLines = 3
	toolResultExpandedMax    = 200
	toolResultExpandedView   = 20
)

// toolResultFullText returns the raw result body for a completed tool card.
func toolResultFullText(b *Block) string {
	if b.meta.ToolError != "" {
		return b.meta.ToolError
	}
	raw := b.meta.ToolResult
	if b.meta.ToolName == "bash" {
		var out struct {
			Stdout   string `json:"stdout"`
			Stderr   string `json:"stderr"`
			ExitCode int    `json:"exitCode"`
		}
		if json.Unmarshal([]byte(raw), &out) == nil {
			if out.Stderr != "" {
				return out.Stderr
			}
			if out.Stdout != "" {
				return out.Stdout
			}
			return fmt.Sprintf("exit %d", out.ExitCode)
		}
	}
	return raw
}

func toolResultNeedsExpand(b *Block) bool {
	text := toolResultFullText(b)
	if len(text) > toolResultCollapsedBytes {
		return true
	}
	inner := 36
	if inner < 1 {
		inner = 1
	}
	return len(wrapLine(text, inner)) > toolResultCollapsedLines
}

func toolCardExpandedResultLines(b *Block, width int) []string {
	text := toolResultFullText(b)
	inner := width - 4
	if inner < 1 {
		inner = 1
	}
	lines := wrapLine(text, inner)
	if len(lines) > toolResultExpandedMax {
		lines = lines[:toolResultExpandedMax]
	}
	start := b.meta.ToolScroll
	if start < 0 {
		start = 0
	}
	if start > len(lines) {
		start = len(lines)
	}
	end := start + toolResultExpandedView
	if end > len(lines) {
		end = len(lines)
	}
	if start >= end {
		return nil
	}
	return lines[start:end]
}

// toggleToolExpandInView toggles expand on the last completed tool card in view.
func (s *scrollback) toggleToolExpandInView(height, width int) bool {
	id := s.lastToolBlockInView(height, width, true)
	if id == 0 {
		return false
	}
	return s.toggleToolExpandBlock(id)
}

func (s *scrollback) toggleToolExpandBlock(id int64) bool {
	for i := range s.blocks {
		if s.blocks[i].id != id || s.blocks[i].kind != blockToolCall || !s.blocks[i].meta.ToolDone {
			continue
		}
		b := &s.blocks[i]
		if !b.meta.Expanded && !toolResultNeedsExpand(b) {
			return false
		}
		b.meta.Expanded = !b.meta.Expanded
		b.meta.ToolScroll = 0
		if b.meta.Expanded {
			s.focusedToolID = b.id
		} else {
			s.focusedToolID = 0
		}
		b.invalidateLayout()
		return true
	}
	return false
}

// scrollFocusedTool adjusts scroll inside an expanded tool result.
func (s *scrollback) scrollFocusedTool(width, delta int) bool {
	if s.focusedToolID == 0 || delta == 0 {
		return false
	}
	for i := range s.blocks {
		b := &s.blocks[i]
		if b.id != s.focusedToolID || !b.meta.Expanded || !b.meta.ToolDone {
			continue
		}
		inner := width - 4
		if inner < 1 {
			inner = 1
		}
		lines := wrapLine(toolResultFullText(b), inner)
		if len(lines) > toolResultExpandedMax {
			lines = lines[:toolResultExpandedMax]
		}
		maxScroll := len(lines) - toolResultExpandedView
		if maxScroll < 0 {
			maxScroll = 0
		}
		next := b.meta.ToolScroll + delta
		if next < 0 {
			next = 0
		}
		if next > maxScroll {
			next = maxScroll
		}
		if next == b.meta.ToolScroll {
			return false
		}
		b.meta.ToolScroll = next
		b.invalidateLayout()
		return true
	}
	s.focusedToolID = 0
	return false
}

// collapseFocusedTool collapses the focused expanded tool card.
func (s *scrollback) collapseFocusedTool() bool {
	if s.focusedToolID == 0 {
		return false
	}
	for i := range s.blocks {
		b := &s.blocks[i]
		if b.id != s.focusedToolID || !b.meta.Expanded {
			continue
		}
		b.meta.Expanded = false
		b.meta.ToolScroll = 0
		b.invalidateLayout()
		s.focusedToolID = 0
		return true
	}
	s.focusedToolID = 0
	return false
}

func (s *scrollback) focusedToolExpanded(width int) bool {
	if s.focusedToolID == 0 || width < 1 {
		return false
	}
	for i := range s.blocks {
		b := &s.blocks[i]
		if b.id == s.focusedToolID && b.meta.Expanded && b.meta.ToolDone {
			inner := width - 4
			if inner < 1 {
				inner = 1
			}
			lines := wrapLine(toolResultFullText(b), inner)
			if len(lines) > toolResultExpandedMax {
				lines = lines[:toolResultExpandedMax]
			}
			return len(lines) > toolResultExpandedView
		}
	}
	return false
}

func (s *scrollback) lastToolBlockInView(height, width int, requireDone bool) int64 {
	if height < 1 || width < 1 || len(s.blocks) == 0 {
		return 0
	}
	wrapped, start, end := s.viewportRange(height, width)
	seen := map[int64]struct{}{}
	for i := end - 1; i >= start; i-- {
		if i < 0 || i >= len(wrapped) {
			continue
		}
		id := wrapped[i].Tag.BlockID
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		for j := range s.blocks {
			b := &s.blocks[j]
			if b.id != id || b.kind != blockToolCall {
				continue
			}
			if requireDone && !b.meta.ToolDone {
				continue
			}
			return id
		}
	}
	return 0
}