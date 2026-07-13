package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"poisson/internal/provider"
	"poisson/internal/tools"
)

const quickAnswerSystem = `You answer brief side questions while the user continues a main coding session. ` +
	`Be concise and direct. Do not ask follow-up questions unless essential.`

// btwQuestionPrefix wraps a /btw question. It rides in the appended user turn
// (never the system prompt) so the cached system+tools+messages prefix stays
// byte-identical to the main agent's request and hits the cache.
const btwQuestionPrefix = "[Side question from the user — answer directly and concisely using the conversation above for context. " +
	"You may use read-only tools (read, ls, glob, search, exa_search, fetch, recall) if it helps answer accurately, " +
	"but do not call any other tool.]\n\n"

// btwAllowedTools are the only tools /btw's side-question loop will actually
// execute — read-only introspection, never approval-gated. bash/edit/write/
// subagent are deliberately excluded even though the full tool schema is
// still sent (see StreamQuickAnswer): a call to any of them is denied, not
// executed. Two independent reasons, not one: (1) the approval overlay is a
// single shared slot, and /btw's own side panel is ITSELF an overlay — a
// bash approval prompt would destroy it mid-answer; (2) mutating the
// filesystem or spawning a child agent from an unaudited, never-persisted
// side channel that can run concurrently with the main turn's own tool
// calls is a bad property regardless of how easy the approval plumbing
// would be to wire up.
var btwAllowedTools = map[string]bool{
	"read":       true,
	"ls":         true,
	"glob":       true,
	"search":     true,
	"exa_search": true,
	"fetch":      true,
	"recall":     true,
}

// btwMaxToolRounds caps how many tool-call round-trips one /btw answer can
// take. A brief side question should never legitimately need many; this is
// a backstop against a model that keeps calling tools instead of answering.
const btwMaxToolRounds = 6

// StreamQuickAnswer answers a /btw side question with the full conversation as
// context. It reuses buildRequest's exact system + tools + messages prefix so
// the request hits the main conversation's prompt cache, then appends the
// question as a new user turn. Nothing is written to the session/store or the
// agent output channel. Text deltas stream on textCh; a terminal error (if any)
// on errCh; both close when the goroutine exits. Cancelling ctx stops it.
//
// The model may call read-only tools (see btwAllowedTools) to ground its
// answer in real file/search content; onToolStatus, if non-nil, is called
// with a short description of each tool call as it runs so the caller can
// show live progress. Any other tool the model attempts is denied with a
// tool_result error, never executed.
func (a *Agent) StreamQuickAnswer(ctx context.Context, question string, onToolStatus func(string)) (<-chan string, <-chan error, error) {
	if a == nil || a.provider == nil {
		return nil, nil, fmt.Errorf("agent not configured")
	}
	question = trimSpace(question)
	if question == "" {
		return nil, nil, fmt.Errorf("empty question")
	}

	// Reuse the live conversation prefix (system + tools + messages + effort +
	// cache key) so the side question is answered in context and reuses the
	// prompt cache. Keep the session effort so history thinking-blocks serialize
	// identically (a different thinking-enabled state would change the cached
	// bytes and miss). Fall back to a standalone request before any session
	// exists (e.g. /btw as the very first thing).
	req, err := a.buildRequest()
	if err != nil {
		req = &provider.Request{
			Model:  a.currentModel(),
			System: []provider.SystemBlock{{Text: quickAnswerSystem}},
			Effort: "low",
		}
	}
	req.Messages = append(req.Messages, provider.Message{
		Role: "user",
		Content: []provider.ContentBlock{{
			Type: "text",
			Text: btwQuestionPrefix + question,
		}},
	})

	textCh := make(chan string, 32)
	errCh := make(chan error, 1)
	go func() {
		defer close(textCh)
		defer close(errCh)
		if err := a.runQuickAnswerLoop(ctx, req, textCh, onToolStatus); err != nil {
			errCh <- err
		}
	}()
	return textCh, errCh, nil
}

// runQuickAnswerLoop drives the /btw request through as many rounds as the
// model needs to call read-only tools, streaming text deltas from every
// round onto textCh. Mirrors runTurn's stream-drain shape but scoped to an
// ephemeral, unpersisted conversation copy — nothing here touches a.store.
func (a *Agent) runQuickAnswerLoop(ctx context.Context, req *provider.Request, textCh chan<- string, onToolStatus func(string)) error {
	for round := 0; ; round++ {
		ch, err := a.provider.Stream(ctx, req)
		if err != nil {
			return err
		}

		var textBuilder strings.Builder
		var thinkingBuilder, thinkingSig strings.Builder
		var redactedThinking []provider.ContentBlock
		var toolCalls []provider.ToolCall
		var streamErr error

		for ev := range ch {
			switch ev.Type {
			case provider.EventTextDelta:
				textBuilder.WriteString(ev.Text)
				if ev.Text != "" {
					textCh <- ev.Text
				}
			case provider.EventThinkingDelta:
				thinkingBuilder.WriteString(ev.Text)
			case provider.EventThinkingSignature:
				thinkingSig.WriteString(ev.Text)
			case provider.EventThinkingRedacted:
				redactedThinking = append(redactedThinking, provider.ContentBlock{
					Type: "thinking", Redacted: true, ThinkingSignature: ev.Text,
				})
			case provider.EventToolUseStart:
				if ev.ToolCall != nil {
					toolCalls = append(toolCalls, *ev.ToolCall)
				}
			case provider.EventToolUseDelta:
				a.updateToolCall(toolCalls, ev.ToolCall)
			case provider.EventToolUseStop:
				a.updateToolCall(toolCalls, ev.ToolCall)
			case provider.EventError:
				streamErr = ev.Error
			case provider.EventDone:
			}
		}
		if streamErr != nil {
			return streamErr
		}
		if len(toolCalls) == 0 || round >= btwMaxToolRounds {
			// Done — either the model gave its final answer, or it's out of
			// rounds and whatever text it already streamed this round (if
			// any) has to stand as the answer.
			return nil
		}

		assistantBlocks := buildAssistantBlocks(
			thinkingBuilder.String(), thinkingSig.String(), redactedThinking,
			textBuilder.String(), toolCalls)
		req.Messages = append(req.Messages, provider.Message{Role: "assistant", Content: assistantBlocks})

		for _, tc := range toolCalls {
			block := provider.ContentBlock{Type: "tool_result", ToolCallID: tc.ID}
			if !btwAllowedTools[tc.Name] {
				block.ToolIsError = true
				block.ToolResult = "tool not available for /btw side questions (read-only tools only)"
			} else {
				if onToolStatus != nil {
					onToolStatus(btwToolStatusText(tc.Name, tc.Input))
				}
				callCtx := tools.WithToolCallID(ctx, tc.ID)
				res, execErr := a.tools.Execute(callCtx, tc.Name, tc.Input)
				if execErr != nil {
					res = tools.TrimToolResult(tools.ToolResult{Error: execErr.Error()})
				}
				if res.Error != "" {
					block.ToolIsError = true
					block.ToolResult = res.Error
				} else {
					block.ToolResult = res.Content
				}
			}
			req.Messages = append(req.Messages, provider.Message{Role: "tool", Content: []provider.ContentBlock{block}})
		}
	}
}

// btwToolStatusText formats a short "name(arg)" description of a tool call
// for progress display, using whichever of a handful of common input field
// names is present; falls back to the bare tool name.
func btwToolStatusText(name string, input json.RawMessage) string {
	var args map[string]any
	if json.Unmarshal(input, &args) == nil {
		for _, key := range []string{"path", "pattern", "query", "url"} {
			if v, ok := args[key].(string); ok && v != "" {
				return name + "(" + v + ")"
			}
		}
	}
	return name
}

func trimSpace(s string) string {
	i, j := 0, len(s)
	for i < j && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n') {
		i++
	}
	for j > i && (s[j-1] == ' ' || s[j-1] == '\t' || s[j-1] == '\n') {
		j--
	}
	return s[i:j]
}
