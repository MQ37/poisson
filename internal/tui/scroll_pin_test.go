package tui

import (
	"fmt"
	"strings"
	"testing"

	"poisson/internal/agent"
)

// Regression suite for: scrolling up in the conversation while the agent is
// streaming a response must NOT keep dragging the view back down to the
// bottom. scrollOffset is defined as "rows from the bottom" — with a naive
// implementation the bottom keeps moving as new lines stream in, so a FIXED
// offset silently shows a different (later) slice of content on every single
// streamed chunk, even though the user never touched a scroll key. The fix
// (compensateGrowth, in scrollback.go) increases scrollOffset by exactly the
// number of newly streamed rows whenever the user is scrolled up, so the
// absolute content shown stays fixed until they scroll again.

// visibleTexts is a small helper returning the plain (ANSI-stripped) text of
// every row currently in the viewport, for content-identity comparisons.
func visibleTexts(rows []ScreenRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = stripANSI(r.Text)
	}
	return out
}

func sameTexts(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestScrollbackPinnedContentStableAcrossStreamingGrowth is the core
// regression test: scroll up mid-conversation, then stream several more
// chunks of assistant text (simulating the agent still generating), and
// assert the exact same lines stay visible after every single chunk.
func TestScrollbackPinnedContentStableAcrossStreamingGrowth(t *testing.T) {
	s := newScrollback(1024)
	// Enough distinct, numbered lines that scrolling up lands well clear of
	// both the top and the (still growing) bottom.
	for i := 0; i < 60; i++ {
		s.append(StyledLine{Style: styleAssistant, Text: fmt.Sprintf("line %02d\n", i)})
	}

	const height, width = 10, 40
	s.clampScrollOffset(height, width)
	s.scrollUp(20) // scroll well up into history
	s.clampScrollOffset(height, width)
	if s.scrollOffset == 0 {
		t.Fatal("expected to be scrolled up, not pinned")
	}

	before := visibleTexts(s.visible(height, width))
	if len(before) == 0 {
		t.Fatal("expected a non-empty viewport")
	}

	// Simulate the agent streaming many more lines, one render tick at a
	// time — exactly the scenario reported: content keeps growing at the
	// tail while the user is scrolled up reading history.
	for i := 0; i < 30; i++ {
		s.append(StyledLine{Style: styleAssistant, Text: fmt.Sprintf("streamed %02d\n", i)})
		// A real paint() tick calls clampScrollOffset via viewportRange on
		// every render, streaming or not — replicate that here.
		s.clampScrollOffset(height, width)
		after := visibleTexts(s.visible(height, width))
		if !sameTexts(before, after) {
			t.Fatalf("view shifted after streamed line %d:\n before=%v\n after=%v", i, before, after)
		}
	}
}

// TestScrollbackPinnedFollowsBottomWhenNotScrolled verifies the OTHER half of
// the contract still works: when the user has NOT scrolled up (pinned to the
// live tail), new streamed content must show up immediately, same as before
// this fix.
func TestScrollbackPinnedFollowsBottomWhenNotScrolled(t *testing.T) {
	s := newScrollback(1024)
	for i := 0; i < 20; i++ {
		s.append(StyledLine{Style: styleAssistant, Text: fmt.Sprintf("line %02d\n", i)})
	}
	const height, width = 5, 40
	s.clampScrollOffset(height, width)
	if s.scrollOffset != 0 {
		t.Fatalf("expected pinned (offset 0), got %d", s.scrollOffset)
	}

	s.append(StyledLine{Style: styleAssistant, Text: "brand new tail line\n"})
	s.clampScrollOffset(height, width)
	rows := s.visible(height, width)
	var joined strings.Builder
	for _, r := range rows {
		joined.WriteString(stripANSI(r.Text))
		joined.WriteByte('\n')
	}
	if !strings.Contains(joined.String(), "brand new tail line") {
		t.Fatalf("pinned view should follow new content to the bottom, visible rows = %q", joined.String())
	}
}

// TestScrollbackCompensationSkippedAcrossWidthChange verifies a resize (width
// change) is not confused with tail growth — clampScrollOffset's normal
// max-based clamping applies instead, exactly as before this fix.
func TestScrollbackCompensationSkippedAcrossWidthChange(t *testing.T) {
	s := newScrollback(1024)
	for i := 0; i < 40; i++ {
		s.append(StyledLine{Style: styleAssistant, Text: fmt.Sprintf("line %02d\n", i)})
	}
	s.clampScrollOffset(10, 40)
	s.scrollUp(15)
	s.clampScrollOffset(10, 40)
	offsetBefore := s.scrollOffset

	// Resize to a much wider terminal: re-wrapping changes the total row
	// count for reasons that have nothing to do with new content, so this
	// must NOT trigger growth compensation (which would over-adjust the
	// offset based on a spurious "growth" from more/fewer wrapped rows).
	s.clampScrollOffset(10, 120)

	// The offset may legitimately get clamped down if 120-wide wrapping has
	// fewer total rows than the old offset allows, but it must never grow —
	// compensateGrowth only ever adds when the width matches the last check.
	if s.scrollOffset > offsetBefore {
		t.Fatalf("offset grew across a width change: %d -> %d (compensation must not apply)", offsetBefore, s.scrollOffset)
	}
}

// TestScrollbackNoDriftWithoutGrowth verifies repeated clampScrollOffset
// calls with no new content never change scrollOffset (idempotent — no
// spurious drift from calling the compensation path every render tick).
func TestScrollbackNoDriftWithoutGrowth(t *testing.T) {
	s := newScrollback(1024)
	for i := 0; i < 30; i++ {
		s.append(StyledLine{Style: styleAssistant, Text: fmt.Sprintf("line %02d\n", i)})
	}
	s.clampScrollOffset(10, 40)
	s.scrollUp(10)
	s.clampScrollOffset(10, 40)
	offset := s.scrollOffset

	for i := 0; i < 20; i++ {
		s.clampScrollOffset(10, 40)
		if s.scrollOffset != offset {
			t.Fatalf("offset drifted on tick %d with no new content: %d -> %d", i, offset, s.scrollOffset)
		}
	}
}

// TestScrollbackCompensationAcrossManySmallStreamedChunks mirrors real
// token-by-token streaming: many tiny appends to the SAME growing assistant
// block (merged via the streaming-kinds path), rather than whole new lines.
func TestScrollbackCompensationAcrossManySmallStreamedChunks(t *testing.T) {
	s := newScrollback(1024)
	for i := 0; i < 40; i++ {
		s.append(StyledLine{Style: styleAssistant, Text: fmt.Sprintf("history line %02d\n", i)})
	}
	const height, width = 8, 30
	s.clampScrollOffset(height, width)
	s.scrollUp(25)
	s.clampScrollOffset(height, width)
	before := visibleTexts(s.visible(height, width))

	// Stream one long reply in small word-sized chunks, appended to the same
	// tail block (the merge path in appendBlock), each followed by a render
	// tick — this is the literal shape of live token streaming.
	words := strings.Fields(strings.Repeat("token wave crashes softly against the shore of context ", 40))
	for _, w := range words {
		s.append(StyledLine{Style: styleAssistant, Text: w + " "})
		s.clampScrollOffset(height, width)
		after := visibleTexts(s.visible(height, width))
		if !sameTexts(before, after) {
			t.Fatalf("view shifted mid-stream at word %q:\n before=%v\n after=%v", w, before, after)
		}
	}
}

// TestTUIScrollPinnedDuringLiveStreamingDoesNotAutoScroll is the end-to-end
// regression test: drives the real paint() pipeline (not just the
// scrollback layer in isolation) through a scripted sequence — populate the
// conversation, scroll up, then simulate the agent streaming several more
// chunks exactly the way the real run loop does (handleEvent + markAfterEvent
// per OutputText event, then a render tick) — and asserts the rendered
// screen (via the vterm harness) shows the exact same scroll-region content
// throughout, never drifting toward the bottom on its own.
func TestTUIScrollPinnedDuringLiveStreamingDoesNotAutoScroll(t *testing.T) {
	tui, v, tick := newGeometryTestTUI(t, "scroll-pin-test")

	tui.mu.Lock()
	for i := 0; i < 60; i++ {
		tui.scroll.append(StyledLine{Style: styleAssistant, Text: fmt.Sprintf("history line %02d\n", i)})
	}
	tui.status.Thinking = true
	tui.mu.Unlock()
	tick()

	// Scroll well up into history via the same path a real Page-Up does.
	tui.mu.Lock()
	tui.scrollByDelta(20)
	tui.mu.Unlock()
	tick()

	scrollRegionRows := func() []string {
		var rows []string
		for r := tui.headerRows + 1; r <= tui.headerRows+tui.scrollRows; r++ {
			rows = append(rows, v.visibleRow(r))
		}
		return rows
	}
	before := scrollRegionRows()

	for i := 0; i < 15; i++ {
		tui.mu.Lock()
		ev := agent.OutputEvent{Type: agent.OutputText, Text: fmt.Sprintf("streamed %02d\n", i)}
		tui.handleEvent(ev)
		tui.markAfterEvent(ev)
		// Whatever the exact dirty-tracking decided for this event, a repaint
		// eventually happens every frame while the agent is thinking (status
		// spinner, etc.) — force one here to check the geometry is correct
		// whenever that next repaint lands, not to test dirty-tracking itself.
		tui.dirty.markScrollAll(tui.scrollRows)
		tui.mu.Unlock()
		tick()

		after := scrollRegionRows()
		if !sameTexts(before, after) {
			t.Fatalf("rendered scroll region shifted after streamed chunk %d:\n before=%q\n after=%q", i, before, after)
		}
	}
}
