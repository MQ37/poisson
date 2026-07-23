package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mq37/poisson/internal/agent"
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
	// stay inline via expandAtFilesSegments.
	cleaned, err := t.attachImageRefs(text)
	if err != nil {
		t.appendErrorLocked(err)
		t.editor.setText("")
		t.clearAttachments()
		return nil
	}
	segments, err := expandAtFilesSegments(cleaned)
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
	// cleaned (not the raw text) so a typed @image.png token never shows twice
	// — once as raw text, once via the card below. If the message was image(s)
	// only, there's nothing left to show as a bubble; the card(s) carry it.
	if strings.TrimSpace(cleaned) != "" {
		t.scroll.append(StyledLine{Style: styleUser, Text: cleaned})
	}
	t.appendFileRefCardsLocked(segments)
	t.appendImageRefCardsLocked()
	t.editor.setText("")
	images := t.takeAttachmentsForSend()
	t.startTurn(segments, images...)
	return nil
}

// appendFileRefCardsLocked appends a collapsible "read"-style card for every
// @path reference among segments, right after the user's message bubble —
// live and on resume both go through this same path (hydrate.go calls it too)
// so a file's content is never dumped inline into the message just to be
// visible. Caller must hold t.mu.
func (t *TUI) appendFileRefCardsLocked(segments []agent.TextSegment) {
	for _, seg := range segments {
		if seg.FileRef == "" {
			continue
		}
		id := t.nextToolID
		t.nextToolID++
		t.scroll.appendFileRefCard(id, seg.FileRef, stripFence(seg.Text))
	}
}

// startTurn kicks off an agent turn for an already-displayed prompt, split
// into segments (see agent.TextSegment). The user message and any file-ref
// cards must already be in the scrollback; startTurn only manages the
// in-flight state and the worker goroutine. Caller must hold t.mu.
func (t *TUI) startTurn(segments []agent.TextSegment, images ...agent.ImageAttachment) {
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
			// The footer hint line (Enter:send vs Enter:queue message) is painted
			// inside the input region, not the status region — markStatus() alone
			// leaves it showing the stale "busy" hint until some unrelated
			// keypress happens to dirty the input region too (confirmed bug, fixed
			// here). Deliberately NOT markFull(): a full repaint sends a much
			// bigger single burst of escape sequences, and terminals (kitty has a
			// documented history of this exact bug class: dropped/duplicated
			// output when an app in raw mode sends large chunks of cursor-move +
			// print sequences in one write, e.g. kovidgoyal/kitty#6306) can
			// mis-render large bursts. Keep this repaint as small as the bug fix
			// actually requires.
			t.dirty.markInput()
			// Send anything queued during this turn as one combined follow-up.
			t.drainQueueLocked()
			t.mu.Unlock()
		}()
		_ = t.agent.PromptSegmentsWithContext(ctx, segments, images...)
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
		// /btw is a side question meant to run *alongside* a live turn (its own
		// one-shot request, no shared session/turn state), so dispatch it now.
		// /name only writes the session's title (store metadata, not turn/message
		// state), so it's equally safe to run immediately. Every other command
		// mutates turn/session state, so keep blocking those.
		if fields := strings.Fields(trimmed); len(fields) > 0 && (fields[0] == "/btw" || fields[0] == "/name") {
			t.editor.setText("")
			t.history = append(t.history, text)
			t.histIdx = -1
			t.draftSaved = ""
			if err := t.handleSlash(trimmed); err != nil {
				t.appendErrorLocked(err)
			}
			return
		}
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
//
// This is the guaranteed-delivery backstop for whatever startTurn's agent
// goroutine (via TakeQueuedForInjection) didn't manage to splice into the
// still-running turn before it ended — normally that's everything, since a
// queued message is polled at every tool-round boundary and right before the
// turn would otherwise conclude, so by the time this defer runs the queue is
// usually already empty. It's only non-empty here for the same reason it's
// ever non-empty: a message queued in the brief gap after the last poll but
// before PromptSegmentsWithContext actually returned.
func (t *TUI) drainQueueLocked() {
	combined, ok := t.combineAndDisplayQueuedLocked()
	if !ok {
		return
	}
	t.startTurn(combined)
}

// combineAndDisplayQueuedLocked drains t.queued (no-op, ok=false if empty or
// mid-compaction — can't submit then; leave it queued for later) and combines
// every queued message into one set of segments, appending ONE scrollback
// bubble for all of them combined rather than one per message: they're
// already sent (and stored) as a single combined turn, and hydrate.go always
// reconstructs one stored user row as one bubble on resume — showing N
// separate live bubbles for what resume always shows as one would make live
// and resume disagree. Caller must hold t.mu.
func (t *TUI) combineAndDisplayQueuedLocked() ([]agent.TextSegment, bool) {
	if len(t.queued) == 0 || t.compacting.Load() {
		return nil, false
	}
	msgs := t.queued
	t.queued = nil
	var combined []agent.TextSegment
	display := make([]string, 0, len(msgs))
	for i, m := range msgs {
		segs, err := expandAtFilesSegments(m)
		if err != nil {
			segs = []agent.TextSegment{{Text: m}}
		}
		if i > 0 {
			combined = append(combined, agent.TextSegment{Text: "\n\n"})
		}
		combined = append(combined, segs...)
		display = append(display, m)
	}
	t.scroll.append(StyledLine{Style: styleUser, Text: strings.Join(display, "\n\n")})
	t.appendFileRefCardsLocked(combined)
	t.scroll.scrollToBottom()
	return combined, true
}

// TakeQueuedForInjection is the agent's pendingInputFn: runTurn polls this
// (from its own goroutine — same cross-goroutine-call-into-TUI pattern as
// Approve) at each iteration boundary so a message queued mid-turn reaches
// the model at the next opportunity instead of only once the whole turn
// (which may run many tool rounds, sometimes for many minutes) finishes.
func (t *TUI) TakeQueuedForInjection() ([]agent.TextSegment, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	combined, ok := t.combineAndDisplayQueuedLocked()
	if !ok {
		return nil, false
	}
	// The pending-preview area (part of the input region) shrinks back down
	// now that the queue's empty — same "size changed" repaint enqueueLocked
	// does when it grows.
	t.dirty.markFull()
	return combined, true
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
	case agent.OutputSubagentProgress:
		t.scroll.updateSubagentProgress(ev.ToolCallID, ev.SubagentTurns, ev.ContextTokens, ev.ContextWindow, ev.Text)
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
	case agent.OutputRetrying:
		// Reuses the same neutral "transient, expected" styling as compacting
		// — not red/error-styled, since a network blip that's actively being
		// retried isn't a failure, and this fires at most twice per outage
		// (start + recovery), never once per backoff attempt.
		t.scroll.appendRaw(styleCompacting, "  "+ev.Text)
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
//
// origin == agent.ApprovalOriginBTW gets different overlay handling: /btw's
// side panel is itself an overlay (t.activeOverlay), and this approval fires
// WHILE that panel is showing and its underlying quick-answer stream is still
// running. Every other origin replaces + cancels whatever overlay work was
// active, same as always; btw's is preserved and restored once the human
// answers, instead of being destroyed mid-answer.
func (t *TUI) Approve(command, description, workdir string, risk agent.BashRisk, origin agent.ApprovalOrigin) (bool, string) {
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
	var resumeBTW *btwOverlay
	if origin == agent.ApprovalOriginBTW {
		if b, ok := t.activeOverlay.(*btwOverlay); ok {
			resumeBTW = b // keep its stream alive; restore it once answered
		}
	}
	if resumeBTW == nil {
		t.cancelOverlayWork()
	}
	overlay := newApprovalOverlay(command, description, workdir, origin)
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
	// A /btw approval must survive the main turn's own cancellation — the two
	// run concurrently and Esc-cancelling the main turn has nothing to do
	// with whether the side question's own command should proceed.
	if runCtx != nil && resumeBTW == nil {
		cancelCh = runCtx.Done()
	}

	restoreOverlay := func() {
		if resumeBTW != nil {
			t.activeOverlay = resumeBTW
		} else {
			t.activeOverlay = nil
		}
	}

	var reply approvalReply
	select {
	case reply = <-t.approvalAnswer:
	case <-t.done:
		t.mu.Lock()
		restoreOverlay()
		t.lastOverlayLines = 0
		t.mu.Unlock()
		return false, ""
	case <-cancelCh:
		reply = approvalReply{Allowed: false}
	}

	t.mu.Lock()
	restoreOverlay()
	t.lastOverlayLines = 0
	t.markScrollDirty()
	t.mu.Unlock()
	return reply.Allowed, reply.Reason
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
