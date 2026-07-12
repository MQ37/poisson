package tui

import (
	"strings"
	"testing"

	"poisson/internal/agent"
)

func TestAppendCompactionNoticeKeepsScrollback(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	tui.mu.Lock()
	tui.scroll.append(StyledLine{Style: styleUser, Text: "hello"})
	tui.scroll.append(StyledLine{Style: styleAssistant, Text: "world"})
	tui.scroll.appendRaw(styleCompacting, "  compacting context...")
	tui.appendCompactionNoticeLocked(12000, 3400)
	tui.mu.Unlock()

	out := testScrollOutput(tui)
	if !strings.Contains(out, "hello") || !strings.Contains(out, "world") {
		t.Fatalf("scrollback cleared: %q", out)
	}
	if strings.Contains(out, "compacting context") {
		t.Fatalf("in-progress notice should be removed: %q", out)
	}
	if !strings.Contains(out, "12,000") || !strings.Contains(out, "3,400") {
		t.Fatalf("expected token notice, got %q", out)
	}
}

func TestHandleEventCompactedAppendsNotice(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	tui.mu.Lock()
	tui.scroll.append(StyledLine{Style: styleUser, Text: "keep me"})
	tui.handleEvent(agent.OutputEvent{
		Type:                   agent.OutputCompacted,
		CompactionTokensBefore: 9000,
		CompactionTokensAfter:  2100,
	})
	tui.mu.Unlock()

	out := testScrollOutput(tui)
	if !strings.Contains(out, "keep me") {
		t.Fatalf("scrollback cleared: %q", out)
	}
	if !strings.Contains(out, "9,000 → 2,100 tokens") {
		t.Fatalf("expected compaction notice, got %q", out)
	}
}
