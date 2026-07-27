package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/tools"
)

const quickAnswerSystem = `You answer brief side questions while the user continues a main coding session. ` +
	`Be concise and direct. Do not ask follow-up questions unless essential.`

// btwQuestionPrefix wraps a /btw question. It rides in the appended user turn
// (never the system prompt) so the cached system+tools+messages prefix stays
// byte-identical to the main agent's request and hits the cache.
const btwQuestionPrefix = "[Side question from the user — answer directly and concisely using the conversation above for context. " +
	"You may use read, bash, web_search, web_ask, and fetch/recall if it helps answer accurately — " +
	"a bash command that needs human approval will pause for it, same as in the main conversation — " +
	"but do not call any other tool.]\n\n"

// btwAllowedTools are the only tools /btw's side-question loop will actually
// execute; any other tool call is denied, not executed, even though the full
// tool schema is still sent (see StreamQuickAnswer). bash is gated by the
// exact same approval mechanism as the main conversation (guard fast path,
// LLM risk classification, human approval when neither auto-approves) — see
// WrapRiskGatedApproval and tui.TUI.Approve's origin-aware overlay handling,
// which keeps /btw's own side panel alive (and resumes it) instead of being
// destroyed by a concurrent approval prompt. edit/write/subagent remain
// excluded: mutating the filesystem or spawning a child agent from an
// unaudited, never-persisted side channel that can run concurrently with the
// main turn's own tool calls is a bad property regardless of approval.
var btwAllowedTools = map[string]bool{
	"read":       true,
	"bash":       true,
	"web_search": true,
	"web_ask":    true,
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
// The model may call a limited set of tools (see btwAllowedTools) — read-only
// introspection plus bash, gated by the normal approval mechanism — to
// ground its answer in real file/command output; onToolStatus, if non-nil,
// is called with a short description of each tool call as it runs so the
// caller can show live progress. Any other tool the model attempts is
// denied with a tool_result error, never executed.
func (a *Agent) StreamQuickAnswer(ctx context.Context, question string, onToolStatus func(string)) (<-chan string, <-chan error, error) {
	if a == nil || a.provider == nil {
		return nil, nil, fmt.Errorf("agent not configured")
	}
	question = strings.TrimSpace(question)
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
	// The live turn may be mid-flight: its assistant message with tool_use
	// blocks (e.g. a still-running subagent) is stored before the matching
	// tool_result is (see agent.go's runTurn — append, then wg.Wait()). If a
	// /btw fires in that window, the copy of history buildRequest just handed
	// back ends in an unresolved tool_use, and the API rejects a request that
	// doesn't immediately follow it with a tool_result. Always fork a safe
	// copy here: synthesize placeholder results for any pending calls and
	// fold them into the same user turn as the question itself, rather than
	// a separate message — a bare extra user message right after would be
	// two user turns in a row, which the API also rejects.
	questionBlock := provider.ContentBlock{Type: "text", Text: btwQuestionPrefix + question}
	blocks := append(pendingToolResultBlocks(req.Messages), questionBlock)
	req.Messages = append(req.Messages, provider.Message{Role: "user", Content: blocks})

	textCh := make(chan string, 32)
	errCh := make(chan error, 1)
	go func() {
		defer close(textCh)
		defer close(errCh)
		if err := a.runQuickAnswerLoop(ctx, req, textCh, onToolStatus); quickAnswerReportableError(err) {
			errCh <- err
		}
	}()
	return textCh, errCh, nil
}

// quickAnswerReportableError decides whether an error from runQuickAnswerLoop
// should reach /btw's errCh. A cancelled/expired ctx (from the retry
// policy's own ctx.Err() checks inside streamQuickAnswerRound) is the user's
// own Esc/close, not a failure to report — same policy PromptWithContext
// applies to runTurn's return value (agent.go). Checked with errors.Is on
// the returned error itself, not "is ctx done right now": that weaker,
// live-state check would also swallow a genuine unrelated failure (e.g. a
// permanent auth error) if it happened to be returned while ctx was — for
// any unrelated reason — also done by that point.
func quickAnswerReportableError(err error) bool {
	return err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

// runQuickAnswerLoop drives the /btw request through as many rounds as the
// model needs to call read-only tools, streaming text deltas from every
// round onto textCh. Mirrors runTurn's stream-drain shape but scoped to an
// ephemeral, unpersisted conversation copy — nothing here touches a.store.
func (a *Agent) runQuickAnswerLoop(ctx context.Context, req *provider.Request, textCh chan<- string, onToolStatus func(string)) error {
	for roundNum := 0; ; roundNum++ {
		round, err := a.streamQuickAnswerRound(ctx, req, textCh)
		if err != nil {
			return err
		}
		if len(round.toolCalls) == 0 || roundNum >= btwMaxToolRounds {
			// Done — either the model gave its final answer, or it's out of
			// rounds and whatever text it already streamed this round (if
			// any) has to stand as the answer.
			return nil
		}

		assistantBlocks := buildAssistantBlocks(
			round.thinking, round.thinkingSig, round.redactedThinking,
			round.text, round.toolCalls)
		req.Messages = append(req.Messages, provider.Message{Role: "assistant", Content: assistantBlocks})

		for _, tc := range round.toolCalls {
			block := provider.ContentBlock{Type: "tool_result", ToolCallID: tc.ID}
			var imageBlock *provider.ContentBlock
			if !btwAllowedTools[tc.Name] {
				block.ToolIsError = true
				block.ToolResult = "tool not available for /btw side questions (read-only tools only)"
			} else {
				if onToolStatus != nil {
					onToolStatus(btwToolStatusText(tc.Name, tc.Input))
				}
				// Tag the dispatch context so any approval this call
				// triggers (bash risk gate or a sensitive-path file check)
				// knows it came from /btw, not the main turn — see
				// ApprovalOriginBTW and tui.TUI.Approve.
				callCtx := WithApprovalOrigin(tools.WithToolCallID(ctx, tc.ID), ApprovalOriginBTW)
				res, execErr := a.tools.Execute(callCtx, tc.Name, tc.Input)
				if execErr != nil {
					res = tools.TrimToolResult(tools.ToolResult{Error: execErr.Error()})
				}
				if res.Error != "" {
					block.ToolIsError = true
					block.ToolResult = res.Error
				} else {
					block.ToolResult = res.Content
					// A tool that loaded an image (currently only `read` on
					// an image file — see ToolResult's doc comment) carries
					// it as a sibling content block, same as the main turn
					// loop (agent.go) — every provider already knows how to
					// turn that into real vision input for a "tool"-role
					// message.
					if res.ImagePath != "" {
						imageBlock = &provider.ContentBlock{
							Type: "image", MediaType: res.MediaType,
							ImagePath: res.ImagePath, ImageName: res.ImageName,
						}
					}
				}
			}
			blocks := []provider.ContentBlock{block}
			if imageBlock != nil {
				blocks = append(blocks, *imageBlock)
			}
			req.Messages = append(req.Messages, provider.Message{Role: "tool", Content: blocks})
		}
	}
}

// quickAnswerRoundResult holds one /btw round's collected output. Text has
// already reached textCh as it streamed (see streamQuickAnswerRound); this
// carries what runQuickAnswerLoop still needs to decide whether the round is
// done and, if not, to build the next request.
type quickAnswerRoundResult struct {
	text             string
	thinking         string
	thinkingSig      string
	redactedThinking []provider.ContentBlock
	toolCalls        []provider.ToolCall
}

// streamQuickAnswerRound runs one /btw round under the same mid-stream-error
// and empty-response retry policy runTurn applies to a real turn (see
// stream_retry.go) — a retryable provider error or an empty response is
// retried in place. Transport failures and retryable statuses (429/5xx/529)
// are already retried inside a.provider.Stream by provider.DoWithRetry; this
// adds the layer that structurally cannot see, since by the time either of
// these arrives the response has already started with HTTP 200.
//
// Unlike streamAndCollect (the classifier/compaction driver), this round
// streams text incrementally to textCh as it arrives — a retryable error is
// only actually retried while nothing has reached textCh yet this attempt;
// once any text (or thinking, or a tool call) has streamed out, retrying
// would re-emit it from scratch and duplicate what the user already sees.
func (a *Agent) streamQuickAnswerRound(ctx context.Context, req *provider.Request, textCh chan<- string) (quickAnswerRoundResult, error) {
	midStreamRetries, emptyAttempts := 0, 0
	for {
		if err := ctx.Err(); err != nil {
			return quickAnswerRoundResult{}, err
		}

		ch, err := a.provider.Stream(ctx, req)
		if err != nil {
			return quickAnswerRoundResult{}, err
		}

		var textBuilder strings.Builder
		var thinkingBuilder, thinkingSig strings.Builder
		var redactedThinking []provider.ContentBlock
		var toolCalls []provider.ToolCall
		var usage *provider.Usage
		var streamErr provider.StreamEvent
		hadErr := false

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
				a.updateToolCall(toolCalls, ev.ToolCall, false)
			case provider.EventToolUseStop:
				a.updateToolCall(toolCalls, ev.ToolCall, true)
			case provider.EventError:
				streamErr = ev
				hadErr = true
			case provider.EventDone:
				usage = ev.Usage
			}
		}
		if usage != nil {
			// Recorded per attempt — a retried attempt still spent real
			// tokens against the provider.
			if err := a.recordAuxiliaryAPICall("btw", usage); err != nil {
				return quickAnswerRoundResult{}, fmt.Errorf("record /btw API call: %w", err)
			}
		}

		noContentYet := textBuilder.Len() == 0 && thinkingBuilder.Len() == 0 &&
			len(toolCalls) == 0 && len(redactedThinking) == 0

		if hadErr {
			if shouldRetryMidStream(streamErr, noContentYet, midStreamRetries) {
				midStreamRetries++
				if err := sleepOrDone(ctx, midStreamRetryDelay(midStreamRetries)); err != nil {
					return quickAnswerRoundResult{}, err
				}
				continue
			}
			return quickAnswerRoundResult{}, streamErr.Error
		}

		if noContentYet {
			if emptyAttempts >= maxEmptyResponseRetries {
				return quickAnswerRoundResult{}, fmt.Errorf("model returned empty response")
			}
			emptyAttempts++
			if err := sleepOrDone(ctx, emptyResponseRetryDelay(emptyAttempts)); err != nil {
				return quickAnswerRoundResult{}, err
			}
			continue
		}

		return quickAnswerRoundResult{
			text: textBuilder.String(), thinking: thinkingBuilder.String(), thinkingSig: thinkingSig.String(),
			redactedThinking: redactedThinking, toolCalls: toolCalls,
		}, nil
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

// pendingToolResultBlocks returns a placeholder tool_result block for every
// tool_use in msgs' last message that isn't already resolved — i.e. the
// live turn's still-running tool call(s). Returns nil once the last message
// isn't an assistant tool_use turn, which is the normal case.
func pendingToolResultBlocks(msgs []provider.Message) []provider.ContentBlock {
	if len(msgs) == 0 {
		return nil
	}
	last := msgs[len(msgs)-1]
	if last.Role != "assistant" {
		return nil
	}
	var blocks []provider.ContentBlock
	for _, cb := range last.Content {
		if cb.Type != "tool_use" {
			continue
		}
		blocks = append(blocks, provider.ContentBlock{
			Type:       "tool_result",
			ToolCallID: cb.ToolCallID,
			ToolResult: "[still running in the main conversation — not available yet for this side question]",
		})
	}
	return blocks
}
