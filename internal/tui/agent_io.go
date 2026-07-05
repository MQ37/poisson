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
	if trimmed == "" && len(t.pendingAttachments) == 0 {
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
	// Stage @image.png refs as attachments (stripped from the text); text files
	// stay inline via expandAtFiles.
	cleaned, err := t.attachImageRefs(text)
	if err != nil {
		t.appendErrorLocked(err)
		t.editor.setText("")
		t.clearAttachments()
		return nil
	}
	expanded, err := expandAtFiles(cleaned)
	if err != nil {
		t.appendErrorLocked(err)
		t.editor.setText("")
		t.clearAttachments()
		return nil
	}
	if text != "" {
		t.history = append(t.history, text)
	}
	t.histIdx = -1
	t.draftSaved = ""
	t.scroll.scrollToBottom()
	display := text
	if strings.TrimSpace(display) == "" {
		display = "🖼 (image)"
	}
	t.scroll.append(StyledLine{Style: styleUser, Text: display})
	t.editor.setText("")
	images := t.takeAttachmentsForSend()
	t.startTurn(expanded, images...)
	return nil
}

// startTurn kicks off an agent turn for an already-displayed prompt. The user
// message must already be in the scrollback; startTurn only manages the
// in-flight state and the worker goroutine. Caller must hold t.mu.
func (t *TUI) startTurn(prompt string, images ...agent.ImageAttachment) {
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
			if r := recover(); r != nil {
				t.mu.Lock()
				t.scroll.appendRaw(styleError, fmt.Sprintf("agent panic: %v", r))
				t.mu.Unlock()
			}
			t.mu.Lock()
			t.status.Thinking = false
			t.dirty.markStatus()
			// Send anything queued during this turn as one combined follow-up.
			t.drainQueueLocked()
			t.mu.Unlock()
		}()
		_ = t.agent.PromptWithContext(ctx, prompt, images...)
	}()
}

// enqueueLocked queues a message typed while a turn is in flight. It shows in
// the pending-preview area and is sent (with any other queued messages) once
// the turn finishes. Caller must hold t.mu.
func (t *TUI) enqueueLocked(text string) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		t.editor.setText("")
		return
	}
	if strings.HasPrefix(trimmed, "/") {
		t.setEphemeralHintLocked("can't queue a / command while busy", 2*time.Second)
		return
	}
	t.history = append(t.history, text)
	t.histIdx = -1
	t.draftSaved = ""
	t.queued = append(t.queued, text)
	t.editor.setText("")
	// Input height grows to show the pending preview, so repaint everything.
	t.dirty.markFull()
}

// drainQueueLocked appends every queued message to the conversation and starts
// one combined follow-up turn. No-op if the queue is empty or a compaction is
// running. Caller must hold t.mu.
func (t *TUI) drainQueueLocked() {
	if len(t.queued) == 0 {
		return
	}
	if t.compacting.Load() {
		// Can't submit during compaction; keep them queued for the next drain.
		return
	}
	msgs := t.queued
	t.queued = nil
	parts := make([]string, 0, len(msgs))
	for _, m := range msgs {
		expanded, err := expandAtFiles(m)
		if err != nil {
			expanded = m
		}
		parts = append(parts, expanded)
		t.scroll.append(StyledLine{Style: styleUser, Text: m})
	}
	t.scroll.scrollToBottom()
	t.startTurn(strings.Join(parts, "\n\n"))
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
		if ev.ToolName == "subagent" {
			// Subagents render as a compact widget, never a full tool card, and
			// their internal steps never touch the main conversation.
			id := t.nextToolID
			t.nextToolID++
			name, task := subagentTaskFromInput(ev.ToolInput)
			if name == "" {
				name = "subagent"
			}
			t.scroll.appendSubagentCard(id, ev.ToolCallID, name, task, modelLabel(t.agent))
			break
		}
		id := t.nextToolID
		t.nextToolID++
		t.scroll.appendToolCall(id, ev.ToolCallID, ev.ToolName, ev.ToolInput)
	case agent.OutputToolResult:
		if ev.ToolName == "subagent" {
			t.scroll.completeSubagentCard(ev.ToolCallID, ev.ToolError, 0)
			break
		}
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