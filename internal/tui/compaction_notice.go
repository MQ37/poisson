package tui

import (
	"fmt"
	"strings"
)

const compactingNoticeText = "compacting context"

// removeTrailingCompactingNoticesLocked drops trailing in-progress compaction lines.
func (t *TUI) removeTrailingCompactingNoticesLocked() {
	for len(t.scroll.blocks) > 0 {
		tail := &t.scroll.blocks[len(t.scroll.blocks)-1]
		if tail.kind != blockCompacting && tail.kind != blockSystem {
			break
		}
		raw := strings.TrimSpace(stripANSI(tail.raw))
		if !strings.Contains(raw, compactingNoticeText) {
			break
		}
		t.scroll.blocks = t.scroll.blocks[:len(t.scroll.blocks)-1]
	}
}

// appendCompactionNoticeLocked records a finished compaction without clearing scrollback.
func (t *TUI) appendCompactionNoticeLocked(before, after int) {
	t.removeTrailingCompactingNoticesLocked()
	msg := fmt.Sprintf("  ✓ Context compacted: %s → %s tokens", formatNum(before), formatNum(after))
	t.scroll.appendRaw(styleSystem, msg)
	t.scroll.scrollToBottom()
	t.markScrollDirty()
}