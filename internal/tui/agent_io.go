package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"poisson/internal/agent"
)

const approvalTimeout = 10 * time.Minute

func (t *TUI) submit(text string) error {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		t.editor.setText("")
		return nil
	}
	if t.compacting.Load() {
		t.appendErrorLocked(errors.New("cannot submit while compacting"))
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
		_ = t.agent.PromptWithContext(ctx, expanded)
	}()
	return nil
}

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
	case agent.OutputError:
		t.scroll.appendRaw(styleError, "error: "+ev.Text)
	case agent.OutputCompacting:
		if t.running() {
			t.scroll.appendRaw(styleCompacting, "  compacting context...")
		}
	case agent.OutputCompacted:
		t.refreshScrollbackFromStoreLocked()
	case agent.OutputStatus:
	}
}

// Approve renders an approval prompt for a dangerous bash command and waits
// for the user's answer.
func (t *TUI) Approve(command, description, workdir string) bool {
	t.approvalMu.Lock()
	defer t.approvalMu.Unlock()

	for {
		select {
		case <-t.approvalAnswer:
		default:
			goto drained
		}
	}
drained:

	t.approving.Store(true)
	defer func() {
		t.approving.Store(false)
		for {
			select {
			case <-t.approvalAnswer:
			default:
				return
			}
		}
	}()

	t.mu.Lock()
	t.clearCompletionLocked()
	t.cancelOverlayWork()
	t.activeOverlay = newApprovalOverlay(command, description, workdir)
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
	timedOut := false
	timer := time.NewTimer(approvalTimeout)
	defer timer.Stop()
	select {
	case allowed = <-t.approvalAnswer:
	case <-t.done:
		t.mu.Lock()
		t.activeOverlay = nil
		t.lastOverlayLines = 0
		t.mu.Unlock()
		return false
	case <-cancelCh:
		allowed = false
	case <-timer.C:
		allowed = false
		timedOut = true
	}

	t.mu.Lock()
	t.activeOverlay = nil
	t.lastOverlayLines = 0
	t.scroll.appendRaw(styleSystem, formatApprovalResult(allowed))
	if timedOut {
		t.scroll.appendRaw(styleSystem, "  approval timed out")
	}
	t.markScrollDirty()
	t.mu.Unlock()
	return allowed
}

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