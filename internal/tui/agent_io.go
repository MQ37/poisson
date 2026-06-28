package tui

import (
	"context"
	"fmt"
	"strings"

	"poisson/internal/agent"
)

func (t *TUI) submit(text string) error {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		t.editor.setText("")
		return nil
	}
	if strings.HasPrefix(trimmed, "/") {
		t.scroll.scrollToBottom()
		t.scroll.append(StyledLine{Style: styleUser, Text: text})
		t.editor.setText("")
		t.histIdx = -1
		t.draftSaved = ""
		return t.handleSlash(trimmed)
	}
	expanded, err := expandAtFiles(text)
	if err != nil {
		t.appendErrorLocked(err)
		t.editor.setText("")
		return nil
	}
	t.history = append(t.history, text)
	t.histIdx = -1
	t.draftSaved = ""
	t.scroll.scrollToBottom()
	t.scroll.append(StyledLine{Style: styleUser, Text: text})
	t.editor.setText("")

	// Start the agent turn. feed (our caller) holds t.mu, so we can set
	// status.Thinking directly. The agent runs in its own goroutine; the Run
	// loop drains output events.
	t.status.Thinking = true
	t.status.Hint = ""
	t.markScrollDirty()
	t.dirty.markStatus()

	ctx, cancel := context.WithCancel(context.Background())
	t.cancelMu.Lock()
	t.cancelCtx = ctx
	t.cancelRun = cancel
	t.cancelMu.Unlock()

	go func() {
		defer func() {
			t.cancelMu.Lock()
			t.cancelCtx = nil
			t.cancelRun = nil
			t.cancelMu.Unlock()
			cancel()
			t.mu.Lock()
			t.status.Thinking = false
			t.dirty.markStatus()
			t.mu.Unlock()
			if r := recover(); r != nil {
				t.mu.Lock()
				t.scroll.appendRaw(styleError, fmt.Sprintf("agent panic: %v", r))
				t.mu.Unlock()
			}
		}()
		// The agent sends OutputError on all error paths; the Run loop displays
		// them. We just wait for completion and clean up.
		_ = t.agent.PromptWithContext(ctx, expanded)
	}()
	return nil
}

// handleEvent appends agent output to the scrollback. Caller must hold t.mu.
func (t *TUI) handleEvent(ev agent.OutputEvent) {
	switch ev.Type {
	case agent.OutputText:
		t.scroll.finalizeThinking()
		t.scroll.append(StyledLine{Style: styleAssistant, Text: ev.Text})
	case agent.OutputThinking:
		t.scroll.append(StyledLine{Style: styleThinking, Text: ev.Text})
	case agent.OutputToolStart:
		t.scroll.finalizeThinking()
		id := t.nextToolID
		t.nextToolID++
		t.scroll.appendToolCall(id, ev.ToolCallID, ev.ToolName, ev.ToolInput)
	case agent.OutputToolResult:
		t.scroll.completeToolCall(ev.ToolCallID, ev.ToolResultContent, ev.ToolError, 0)
	case agent.OutputApproval:
		// Approval UI is shown via activeOverlay in Approve().
	case agent.OutputError:
		t.scroll.appendRaw(styleError, "error: "+ev.Text)
	case agent.OutputCompacting:
		t.scroll.appendRaw(styleCompacting, "  compacting context...")
	case agent.OutputStatus:
		// applied in markAfterEvent
	}
}

// Approve renders an approval prompt for a dangerous bash command and waits
// for the user's answer. The input goroutine is the sole stdin reader; it
// routes the answer through t.approvalAnswer when t.approving is set.
func (t *TUI) Approve(command, description string) bool {
	t.approvalMu.Lock()
	defer t.approvalMu.Unlock()

	select {
	case <-t.approvalAnswer:
	default:
	}

	// Signal before paint so the input goroutine routes keys here immediately
	// (running() would otherwise swallow them during tool execution).
	t.approving.Store(true)
	defer t.approving.Store(false)

	t.mu.Lock()
	t.cancelOverlayWork()
	t.activeOverlay = newApprovalOverlay(command, description)
	t.dirty.markFull()
	t.mu.Unlock()

	t.cancelMu.Lock()
	runCtx := t.cancelCtx
	t.cancelMu.Unlock()

	var cancelCh <-chan struct{}
	if runCtx != nil {
		cancelCh = runCtx.Done()
	}

	var allowed bool
	select {
	case allowed = <-t.approvalAnswer:
	case <-t.done:
		t.mu.Lock()
		t.activeOverlay = nil
		t.mu.Unlock()
		return false
	case <-cancelCh:
		allowed = false
	}

	t.mu.Lock()
	t.activeOverlay = nil
	t.scroll.appendRaw(styleSystem, formatApprovalResult(allowed))
	t.markScrollDirty()
	t.mu.Unlock()
	return allowed
}

// navigateHistory loads a previous/next prompt into the editor. dir=-1 is
// older; dir=+1 is newer.
func (t *TUI) navigateHistory(dir int) {
	if len(t.history) == 0 {
		return
	}
	if t.histIdx == -1 {
		t.histIdx = len(t.history)
		t.draftSaved = t.editor.text()
	}
	t.histIdx += dir
	if t.histIdx < 0 {
		t.histIdx = 0
	}
	if t.histIdx >= len(t.history) {
		t.histIdx = len(t.history)
		t.editor.setText(t.draftSaved)
		return
	}
	t.editor.setText(t.history[t.histIdx])
}
