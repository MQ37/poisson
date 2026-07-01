package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"poisson/internal/agent"
)



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
		if ev.ThinkingRedacted {
			t.scroll.appendThinkingRedacted()
		} else {
			t.scroll.append(StyledLine{Style: styleThinking, Text: ev.Text})
		}
		if t.scroll.pinned() {
			t.scroll.scrollToBottom()
		}
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
		t.appendCompactionNoticeLocked(ev.CompactionTokensBefore, ev.CompactionTokensAfter)
		t.agent.UpdateStatus()
		t.syncHeaderFromAgentLocked()
		t.dirty.markStatus()
	case agent.OutputStatus:
	}
}

// Approve renders an approval prompt for a dangerous bash command and waits
// for the user's answer. When risk is already known, the overlay shows it
// immediately without a second LLM call.
func (t *TUI) Approve(command, description, workdir string, risk agent.BashRisk) bool {
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
	overlay := newApprovalOverlay(command, description, workdir)
	if r := bashRiskLabel(risk); r != "" {
		overlay.setRisk(r)
	}
	t.activeOverlay = overlay
	t.dirty.markFull()
	t.mu.Unlock()

	t.cancelMu.Lock()
	runCtx := t.cancelCtx
	t.cancelMu.Unlock()

	var riskCancel context.CancelFunc
	if risk == agent.BashRiskUnknown || risk == "" {
		var riskCtx context.Context
		riskCtx, riskCancel = context.WithTimeout(context.Background(), 45*time.Second)
		go t.assessApprovalRisk(riskCtx, overlay, command, description, workdir)
		defer riskCancel()
	}

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
		t.lastOverlayLines = 0
		t.mu.Unlock()
		return false
	case <-cancelCh:
		allowed = false
	}

	t.mu.Lock()
	t.activeOverlay = nil
	t.lastOverlayLines = 0
	t.markScrollDirty()
	t.mu.Unlock()
	return allowed
}

func bashRiskLabel(risk agent.BashRisk) string {
	switch risk {
	case agent.BashRiskLow:
		return "low"
	case agent.BashRiskMedium:
		return "medium"
	case agent.BashRiskHigh:
		return "high"
	default:
		return ""
	}
}

func (t *TUI) assessApprovalRisk(ctx context.Context, overlay *approvalOverlay, command, description, workdir string) {
	// Default to "failed": if the LLM can't classify (error/timeout/ambiguous),
	// the overlay must say so and the human must decide.
	risk := "failed"
	if t.agent != nil {
		switch t.agent.AssessBashRisk(ctx, command, description, workdir) {
		case agent.BashRiskLow:
			risk = "low"
		case agent.BashRiskMedium:
			risk = "medium"
		case agent.BashRiskHigh:
			risk = "high"
		}
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.approving.Load() {
		return
	}
	ao, ok := t.activeOverlay.(*approvalOverlay)
	if !ok || ao != overlay {
		return
	}
	ao.setRisk(risk)
	t.dirty.markInput()
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