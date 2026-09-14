package orchestrator

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

// turnBatchInterval/turnBatchMaxChars bound how long assistant text
// accumulates before an actual fe.Send call — batched rather than sent per
// token, whichever threshold is hit first (see docs/orchestrator-plan.md
// Step 20). turnBatchMinChars additionally gates the interval path only —
// found via live testing: without it, the interval timer can fire right as
// a tool call's post-result text has only just started streaming back in,
// flushing a single stray character (observed: a lone "u") as its own
// Telegram message. The size path (turnBatchMaxChars) and every
// unconditional flush (before a tool/approval/error/done event, and the
// final flush when the turn ends) are unaffected — this only delays the
// interval-triggered flush until there's something worth sending on its
// own.
const (
	turnBatchInterval    = 3 * time.Second
	turnBatchMaxChars    = 3500
	turnBatchMinChars    = 20
	turnMaxEventTextChars = 8000 // cap on any single event's text/error reaching the frontend
)

// shouldFlushText reports whether batchLen accumulated characters should be
// flushed now, given elapsed time since the last flush — a pure function so
// the interval/size interplay (in particular turnBatchMinChars's fix) is
// unit-testable without any real timing or goroutines.
func shouldFlushText(batchLen int, elapsed time.Duration) bool {
	if batchLen >= turnBatchMaxChars {
		return true
	}
	return batchLen >= turnBatchMinChars && elapsed >= turnBatchInterval
}

// runTurn acquires a global turn slot (posting an explicit "queued" notice
// if one isn't immediately available — never silently blocking with no
// user feedback), starts one turn via the Runtime, and pumps its events to
// the frontend until it ends. Always leaves the instance in a normal status
// afterward (idle, unless the actor itself is shutting down) — a failed
// turn is not itself a reason to consider the instance broken.
func (c *Core) runTurn(ctx context.Context, inst *Instance, cmd Command) {
	select {
	case c.turnSem <- struct{}{}:
	default:
		inst.setStatus(StatusQueued)
		c.safeSend(ctx, inst.Key, Message{Kind: MsgLifecycle, Text: "queued — waiting for a turn slot"})
		select {
		case c.turnSem <- struct{}{}:
		case <-ctx.Done():
			return
		}
	}
	defer func() { <-c.turnSem }()

	inst.setStatus(StatusRunning)
	defer func() {
		if inst.Status() != StatusDead {
			inst.setStatus(StatusIdle)
		}
	}()

	provider, model, _ := strings.Cut(inst.ModelString(), "/")
	spec := TurnSpec{
		SessionID: inst.Meta.SessionID,
		Provider:  provider,
		Model:     model,
		Message:   cmd.Text,
		// Box instances are disposable, confined containers -- yolo is
		// acceptable because a mistake's blast radius is that one throwaway
		// container. Host instances have no isolation at all, so they
		// always take the real Telegram approval round-trip.
		Yolo: IsBoxKind(inst.Meta.Kind),
	}
	turn, err := c.rt.StartTurn(ctx, inst.Meta.Name, spec)
	if err != nil {
		c.safeSend(ctx, inst.Key, Message{Kind: MsgError, Text: fmt.Sprintf("failed to start turn: %v", err)})
		return
	}
	inst.setCurrentTurn(turn)
	defer inst.setCurrentTurn(nil)

	c.pumpTurn(ctx, inst, turn)
}

// pumpTurn drains turn's events, batching assistant text and rendering
// tool calls/approvals/errors as single lines, until a terminal "done"
// event or the process itself ends. If the process ends without ever
// emitting "done" (killed, OOM'd, or panicked with no chance to clean up),
// a terminal notice is synthesized, explicitly distinguishing a deliberate
// stop (Instance.turnKilledByUs) from an actual crash — the same
// distinction commit 7dcad36 had to retrofit onto the subagent system
// after the fact, built in correctly here from the start.
func (c *Core) pumpTurn(ctx context.Context, inst *Instance, turn *Turn) {
	var batch strings.Builder
	lastFlush := time.Now()
	flush := func() {
		if batch.Len() == 0 {
			return
		}
		c.safeSend(ctx, inst.Key, Message{Kind: MsgText, Text: batch.String()})
		batch.Reset()
		lastFlush = time.Now()
	}

	// Tool-call activity edits one running status message in place instead
	// of sending a new message per call — a turn with dozens of tool calls
	// would otherwise flood the topic with one-line traces before the
	// actual answer ever appears. statusMsgID is per-turn (reset to "" by
	// pumpTurn's own fresh local scope each call), so Send/Edit only ever
	// target this turn's own status line, never a stale one from a
	// previous turn.
	var statusMsgID string
	var toolCalls int
	reportTool := func(name string) {
		toolCalls++
		call := "call"
		if toolCalls != 1 {
			call = "calls"
		}
		text := fmt.Sprintf("[tool: %s] (%d %s so far)", name, toolCalls, call)
		if statusMsgID == "" {
			id, err := c.fe.Send(ctx, inst.Key, Message{Kind: MsgLifecycle, Text: text})
			if err != nil {
				log.Printf("orchestrator: send tool status to %+v failed: %v", inst.Key, err)
				return
			}
			statusMsgID = id
			return
		}
		if err := c.fe.Edit(ctx, inst.Key, statusMsgID, text); err != nil {
			// Best-effort fallback per Frontend.Edit's own contract: the
			// message may be too old, or this frontend may not support
			// editing at all — a fresh Send (and tracking ITS id from here
			// on) is exactly as good, just one more message in the topic
			// instead of zero.
			if id, sendErr := c.fe.Send(ctx, inst.Key, Message{Kind: MsgLifecycle, Text: text}); sendErr == nil {
				statusMsgID = id
			}
		}
	}

	var seq uint64
	sawDone := false
	for {
		ev, err := turn.ReadEvent()
		if err != nil {
			break // EOF, or a genuinely fatal read error — the process is gone either way
		}
		if ev == nil {
			continue // a blank line — ReadEvent's own documented contract
		}
		seq++
		event := Event{InstanceName: inst.Meta.Name, Seq: seq, At: time.Now(), ChildEvent: *ev}
		capEventText(&event)

		switch event.Type {
		case "text":
			batch.WriteString(event.Text)
			if shouldFlushText(batch.Len(), time.Since(lastFlush)) {
				flush()
			}
		case "tool":
			flush()
			reportTool(event.Tool)
		case "approval_request":
			flush()
			inst.setStatus(StatusAwaitingApproval)
			inst.setPending(&PendingApproval{Command: event.Command, Description: event.Description, Risk: event.Risk})
			approvalText := fmt.Sprintf("approval needed (risk %s): %s", event.Risk, event.Command)
			if event.Description != "" {
				approvalText += "\n" + event.Description
			}
			approvalText += "\n/approve or /deny <reason>"
			c.safeSend(ctx, inst.Key, Message{Kind: MsgLifecycle, Text: approvalText})
		case "error":
			flush()
			c.safeSend(ctx, inst.Key, Message{Kind: MsgError, Text: event.Error})
		case "done":
			flush()
			sawDone = true
			inst.setPending(nil)
			if !event.Success {
				c.safeSend(ctx, inst.Key, Message{Kind: MsgError, Text: fmt.Sprintf("turn failed: %s", event.Error)})
			}
		}
		if event.Type == "done" {
			break
		}
	}
	flush()
	inst.setPending(nil)

	if !sawDone {
		if inst.consumeTurnKilledByUs() {
			c.safeSend(ctx, inst.Key, Message{Kind: MsgLifecycle, Text: "turn stopped"})
		} else {
			c.safeSend(ctx, inst.Key, Message{Kind: MsgError, Text: "turn ended unexpectedly (crashed, OOM'd, or was killed externally) with no result"})
		}
	}
}

// capEventText bounds how much of any single event's text/error actually
// reaches the frontend, regardless of how much a runaway tool produced.
func capEventText(ev *Event) {
	if len(ev.Text) > turnMaxEventTextChars {
		ev.Text = ev.Text[:turnMaxEventTextChars] + "\n...[truncated]"
	}
	if len(ev.Error) > turnMaxEventTextChars {
		ev.Error = ev.Error[:turnMaxEventTextChars] + "\n...[truncated]"
	}
}
