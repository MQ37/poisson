// Package agent implements the Poisson agent loop (SPEC §17): ingest → build
// context → stream → dispatch tools → compact if needed → commit.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"poisson/internal/config"
	"poisson/internal/guard"
	"poisson/internal/project"
	"poisson/internal/provider"
	"poisson/internal/skills"
	"poisson/internal/store"
	"poisson/internal/tools"
)

// Output event type constants used by the TUI package.
const (
	OutputText       = "text"
	OutputThinking   = "thinking"
	OutputToolStart  = "tool_start"
	OutputToolResult = "tool_result"
	OutputStatus     = "status"
	OutputApproval   = "approval"
	OutputError      = "error"
	OutputCompacting = "compacting"
	OutputCompacted  = "compacted"
	OutputDone       = "done"
)

// OutputEvent is a serialized terminal rendering event. The TUI goroutine
// drains these from the agent's outputChan and renders them.
type OutputEvent struct {
	Type              string          // text | tool_start | tool_result | status | approval | error | compacting
	Text              string          // text | error | compacting
	ToolName          string          // tool_start | tool_result
	ToolCallID        string          // tool_start | tool_result (provider call id)
	ToolInput         json.RawMessage // tool_start
	ToolResultContent string          // tool_result
	ToolError         string          // tool_result
	ContextPct        float64         // status
	ContextTokens     int             // status
	ContextWindow     int             // status
	Cost              float64         // status
	Model             string          // status
	OutputTokens      int             // status
	CacheReadTokens   int             // status
	CacheWriteTokens  int             // status
	CallCount         int             // status
	ToolCalls         int             // status
	ToolErrors        int             // status
	Effort            string          // status
}

// Agent runs the turn loop for a single session.
type Agent struct {
	store      *store.Store
	provider   provider.Provider
	tools      *tools.Registry
	config     *config.Config
	sessionID  string
	outputChan chan OutputEvent
	approvalFn func(command, description, workdir string) bool
	model      string
	effort     string

	// session tool counters for the status bar (reset on SwitchSession).
	sessionToolCalls  int
	sessionToolErrors int

	// pendingResults holds the text of tool results appended in the current
	// iteration, used by ShouldCompact to estimate new tokens.
	pendingResults []string

	skillsEnabled bool
	skills        []skills.Skill
}

// NewAgent creates an Agent ready to process prompts for the given session.
func NewAgent(
	s *store.Store,
	p provider.Provider,
	t *tools.Registry,
	cfg *config.Config,
	sessionID string,
	outputChan chan OutputEvent,
	approvalFn func(command, description, workdir string) bool,
) *Agent {
	a := &Agent{
		store:      s,
		provider:   p,
		tools:      t,
		config:     cfg,
		sessionID:  sessionID,
		outputChan: outputChan,
		approvalFn: approvalFn,
		model:      defaultModel(p, cfg),
	}
	if cfg != nil {
		guard.SetExtraSafe(cfg.Guard.ExtraSafe)
	}
	return a
}

// --- Session management accessors (for TUI slash commands) ---

// Store returns the underlying store (for session/message queries).
func (a *Agent) Store() *store.Store { return a.store }

// SessionID returns the current session ID.
func (a *Agent) SessionID() string { return a.sessionID }

// SwitchSession changes the active session.
func (a *Agent) SwitchSession(sessionID string) {
	a.sessionID = sessionID
	a.sessionToolCalls = 0
	a.sessionToolErrors = 0
}

// SetProvider swaps the provider and persists it on the active session.
func (a *Agent) SetProvider(p provider.Provider) {
	a.provider = p
	sess, err := a.store.GetSession(a.sessionID)
	if err != nil {
		return
	}
	sess.Provider = p.ID()
	sess.UpdatedAt = time.Now().Unix()
	_ = a.store.UpdateSession(sess)
}

// SetModel updates the session's model name and persists it.
func (a *Agent) SetModel(model string) {
	a.model = model
	sess, err := a.store.GetSession(a.sessionID)
	if err != nil {
		return
	}
	sess.Model = model
	sess.UpdatedAt = time.Now().Unix()
	_ = a.store.UpdateSession(sess)
}

// SetConfig swaps the config (for /reload).
func (a *Agent) SetConfig(cfg *config.Config) {
	a.config = cfg
	if cfg != nil {
		guard.SetExtraSafe(cfg.Guard.ExtraSafe)
	}
}

// SetSkills configures skill discovery for the system prompt and skill tool.
// When enabled is false, skills are cleared and the skill tool is removed.
func (a *Agent) SetSkills(enabled bool, sk []skills.Skill) {
	a.skillsEnabled = enabled
	if !enabled || len(sk) == 0 {
		a.skills = nil
		if a.tools != nil {
			a.tools.Unregister("skill")
		}
		return
	}
	a.skills = append([]skills.Skill(nil), sk...)
	if a.tools != nil {
		a.tools.Register(tools.NewSkillTool(a.skills))
	}
}

// SkillsEnabled reports whether skill loading is active for this process.
func (a *Agent) SkillsEnabled() bool { return a.skillsEnabled }

// Skills returns the current skill list (may be nil).
func (a *Agent) Skills() []skills.Skill {
	if len(a.skills) == 0 {
		return nil
	}
	return append([]skills.Skill(nil), a.skills...)
}

// ReloadSkills rediscovers ~/.poisson/skills and refreshes prompt + skill tool.
func (a *Agent) ReloadSkills() (int, error) {
	if !a.skillsEnabled {
		a.skills = nil
		if a.tools != nil {
			a.tools.Unregister("skill")
		}
		return 0, nil
	}
	sk, err := skills.Discover()
	if err != nil {
		return 0, err
	}
	a.SetSkills(true, sk)
	return len(sk), nil
}

// ReloadConfigDependentTools updates tools gated on runtime config (e.g. fetch).
func (a *Agent) ReloadConfigDependentTools() {
	if a.tools == nil || a.config == nil {
		return
	}
	if tools.IsOllamaReachable(a.config) {
		a.tools.Register(tools.NewFetchTool(a.config.Ollama.BaseURL))
	} else {
		a.tools.Unregister("fetch")
	}
}

// Provider returns the current provider.
func (a *Agent) Provider() provider.Provider { return a.provider }

// Config returns the current config.
func (a *Agent) Config() *config.Config { return a.config }

// SetEffort sets the thinking effort for subsequent requests.
func (a *Agent) SetEffort(level string) { a.effort = level }

// Model returns the current model name.
func (a *Agent) Model() string {
	if a.model != "" {
		return a.model
	}
	return defaultModel(a.provider, a.config)
}

// defaultModel returns the configured default for a given provider.
func defaultModel(p provider.Provider, cfg *config.Config) string {
	if cfg == nil || p == nil {
		return ""
	}
	switch p.ID() {
	case "ollama":
		return cfg.Ollama.Model
	case "anthropic":
		return cfg.Anthropic.Model
	case "xai":
		return cfg.XAI.Model
	default:
		return ""
	}
}

// Effort returns the current thinking effort level.
func (a *Agent) Effort() string { return a.effort }

// EnsureSession persists the active session row if it does not exist yet.
// Sessions are created lazily on the first user message, not at process start.
func (a *Agent) EnsureSession() error {
	_, err := a.store.GetSession(a.sessionID)
	if err == nil {
		return nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	cwd, _ := os.Getwd()
	now := time.Now().Unix()
	return a.store.CreateSession(&store.Session{
		ID:        a.sessionID,
		Cwd:       cwd,
		Provider:  a.provider.ID(),
		Model:     a.Model(),
		CreatedAt: now,
		UpdatedAt: now,
	})
}

// Prompt appends the user message to the store and runs the turn loop.
func (a *Agent) Prompt(userInput string) error {
	return a.PromptWithContext(context.Background(), userInput)
}

// PromptWithContext is Prompt with cancellation support.
func (a *Agent) PromptWithContext(ctx context.Context, userInput string) error {
	if err := a.EnsureSession(); err != nil {
		a.sendEvent(OutputEvent{Type: OutputError, Text: fmt.Sprintf("Session error: %v", err)})
		a.sendEvent(OutputEvent{Type: OutputDone})
		return fmt.Errorf("ensure session: %w", err)
	}

	// INGEST: append user message.
	content, err := contentBlocksToJSON([]provider.ContentBlock{
		{Type: "text", Text: userInput},
	})
	if err != nil {
		a.sendEvent(OutputEvent{Type: OutputError, Text: fmt.Sprintf("Marshal error: %v", err)})
		a.sendEvent(OutputEvent{Type: OutputDone})
		return fmt.Errorf("marshal user content: %w", err)
	}
	userMsg := &store.Message{
		SessionID: a.sessionID,
		Role:      "user",
		Content:   content,
	}
	if err := a.store.AppendMessage(userMsg); err != nil {
		a.sendEvent(OutputEvent{Type: OutputError, Text: fmt.Sprintf("Store error: %v", err)})
		a.sendEvent(OutputEvent{Type: OutputDone})
		return fmt.Errorf("append user message: %w", err)
	}

	err = a.runTurn(ctx)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		_ = a.store.SoftDeleteMessages(a.sessionID, userMsg.Seq)
	}
	return err
}

// runTurn executes the turn loop: build → stream → collect tools → dispatch →
// append results → check compaction → repeat until no tool calls.
func (a *Agent) runTurn(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			a.sendEvent(OutputEvent{Type: OutputDone})
			return err
		}
		// BUILD
		req, err := a.buildRequest()
		if err != nil {
			a.sendEvent(OutputEvent{Type: OutputError, Text: fmt.Sprintf("Build error: %v", err)})
			a.sendEvent(OutputEvent{Type: OutputDone})
			return fmt.Errorf("build request: %w", err)
		}

		// CALL
		ch, err := a.provider.Stream(ctx, req)
		if err != nil {
			a.sendEvent(OutputEvent{Type: OutputError, Text: fmt.Sprintf("Provider error: %v", err)})
			a.sendEvent(OutputEvent{Type: OutputDone})
			return fmt.Errorf("stream: %w", err)
		}

		// Drain the stream channel.
		var textBuilder strings.Builder
		var thinkingBuilder strings.Builder
		var thinkingSig strings.Builder
		var redactedThinking []provider.ContentBlock
		var toolCalls []provider.ToolCall
		var usage *provider.Usage

		for ev := range ch {
			switch ev.Type {
			case provider.EventTextDelta:
				textBuilder.WriteString(ev.Text)
				a.sendEvent(OutputEvent{Type: OutputText, Text: ev.Text})

			case provider.EventThinkingDelta:
				thinkingBuilder.WriteString(ev.Text)
				a.sendEvent(OutputEvent{Type: OutputThinking, Text: ev.Text})

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

			case provider.EventDone:
				usage = ev.Usage

			case provider.EventError:
				a.sendEvent(OutputEvent{Type: OutputError, Text: ev.Error.Error()})
				a.sendEvent(OutputEvent{Type: OutputDone})
				return fmt.Errorf("stream error: %w", ev.Error)
			}
		}

		if err := ctx.Err(); err != nil {
			a.sendEvent(OutputEvent{Type: OutputDone})
			return err
		}

		// COMMIT: record api_call (exact usage + cost).
		var apiCallID string
		if usage != nil {
			id, err := a.recordAPICall(usage)
			if err != nil {
				log.Printf("warning: record api call: %v", err)
			} else {
				apiCallID = id
			}
		}

		// COMMIT: append assistant message.
		assistantBlocks := buildAssistantBlocks(
			thinkingBuilder.String(), thinkingSig.String(), redactedThinking,
			textBuilder.String(), toolCalls)
		assistantContent, err := contentBlocksToJSON(assistantBlocks)
		if err != nil {
			a.sendEvent(OutputEvent{Type: OutputError, Text: fmt.Sprintf("Marshal error: %v", err)})
			a.sendEvent(OutputEvent{Type: OutputDone})
			return fmt.Errorf("marshal assistant content: %w", err)
		}
		msg := &store.Message{
			SessionID: a.sessionID,
			Role:      "assistant",
			Content:   assistantContent,
		}
		if apiCallID != "" {
			msg.APICallID = &apiCallID
		}
		if err := a.store.AppendMessage(msg); err != nil {
			a.sendEvent(OutputEvent{Type: OutputError, Text: fmt.Sprintf("Store error: %v", err)})
			a.sendEvent(OutputEvent{Type: OutputDone})
			return fmt.Errorf("append assistant message: %w", err)
		}

		// Update status bar.
		a.UpdateStatus()

		// If the model didn't call any tools, the turn is done.
		if len(toolCalls) == 0 {
			a.sendEvent(OutputEvent{Type: OutputDone})
			break
		}

		// TOOLS: notify TUI of tool starts.
		for _, tc := range toolCalls {
			a.sendEvent(OutputEvent{
				Type:       OutputToolStart,
				ToolName:   tc.Name,
				ToolCallID: tc.ID,
				ToolInput:  tc.Input,
			})
		}

		// Dispatch concurrently; emit each tool_result to the TUI as it finishes
		// so cards stop spinning without waiting for slower siblings (e.g. bash approval).
		a.pendingResults = nil
		results := make([]tools.ToolResult, len(toolCalls))
		var wg sync.WaitGroup
		for i, tc := range toolCalls {
			wg.Add(1)
			go func(idx int, call tools.ToolCall) {
				defer wg.Done()
				res, err := a.tools.Execute(ctx, call.Name, call.Input)
				if err != nil {
					res = tools.TrimToolResult(tools.ToolResult{Error: err.Error()})
				}
				results[idx] = res
				a.sendEvent(OutputEvent{
					Type:              OutputToolResult,
					ToolName:          call.Name,
					ToolCallID:        call.ID,
					ToolResultContent: res.Content,
					ToolError:         res.Error,
				})
			}(i, tc)
		}
		wg.Wait()
		if err := ctx.Err(); err != nil {
			a.sendEvent(OutputEvent{Type: OutputDone})
			return err
		}

		// Persist tool_result messages in start order.
		for i, result := range results {
			toolBlock := provider.ContentBlock{
				Type:       "tool_result",
				ToolCallID: toolCalls[i].ID,
			}
			if result.Error != "" {
				toolBlock.ToolIsError = true
				toolBlock.ToolResult = result.Error
			} else {
				toolBlock.ToolResult = result.Content
			}

			toolContent, err := contentBlocksToJSON([]provider.ContentBlock{toolBlock})
			if err != nil {
				a.sendEvent(OutputEvent{Type: OutputError, Text: fmt.Sprintf("Marshal error: %v", err)})
				a.sendEvent(OutputEvent{Type: OutputDone})
				return fmt.Errorf("marshal tool result: %w", err)
			}
			if err := a.store.AppendMessage(&store.Message{
				SessionID: a.sessionID,
				Role:      "tool",
				Content:   toolContent,
			}); err != nil {
				a.sendEvent(OutputEvent{Type: OutputError, Text: fmt.Sprintf("Store error: %v", err)})
				a.sendEvent(OutputEvent{Type: OutputDone})
				return fmt.Errorf("append tool result message: %w", err)
			}

			pending := toolBlock.ToolResult
			if toolBlock.ToolIsError {
				pending = "Error: " + pending
			}
			a.pendingResults = append(a.pendingResults, pending)

			a.sessionToolCalls++
			if result.Error != "" {
				a.sessionToolErrors++
			}

		}

		a.UpdateStatus()

		// CHECK COMPACTION
		if a.shouldCompact() {
			if err := a.compact(ctx, true); err != nil {
				a.sendEvent(OutputEvent{Type: OutputError, Text: fmt.Sprintf("Compaction error: %v", err)})
				a.sendEvent(OutputEvent{Type: OutputDone})
				return fmt.Errorf("compaction failed: %w", err)
			}
		}

		// Loop: re-stream with updated context (tool results now in store).
	}

	return nil
}

// buildRequest assembles a provider.Request from the store: active messages,
// system prompt, compaction summary (if set), and tool definitions.
func (a *Agent) buildRequest() (*provider.Request, error) {
	// Get session.
	sess, err := a.store.GetSession(a.sessionID)
	if err != nil {
		return nil, fmt.Errorf("get session: %w", err)
	}

	// Get active messages (deleted_at IS NULL AND compacted = 0).
	msgs, err := a.store.GetMessages(a.sessionID)
	if err != nil {
		return nil, fmt.Errorf("get messages: %w", err)
	}

	// Convert store messages to provider messages.
	providerMsgs := make([]provider.Message, 0, len(msgs))
	for _, m := range msgs {
		pm, err := messageToProvider(m)
		if err != nil {
			return nil, fmt.Errorf("convert message %s: %w", m.ID, err)
		}
		providerMsgs = append(providerMsgs, pm)
	}

	// Build system prompt with AGENTS.md + skills.
	agentDir := config.ConfigDir()
	contextFiles := project.LoadProjectContextFiles(sess.Cwd, agentDir)
	toolNames := make([]string, 0)
	if a.tools != nil {
		for _, td := range a.tools.Definitions() {
			toolNames = append(toolNames, td.Name)
		}
	}
	var skillsText string
	if a.skillsEnabled && len(a.skills) > 0 {
		skillsText = skills.FormatSkillsForPrompt(a.skills)
	}
	sysPrompt := project.BuildSystemPrompt(project.BuildSystemPromptOptions{
		Cwd:          sess.Cwd,
		ToolNames:    toolNames,
		ContextFiles: contextFiles,
		SkillsText:   skillsText,
	})

	var systemBlocks []provider.SystemBlock
	systemBlocks = append(systemBlocks, provider.SystemBlock{
		Text: sysPrompt,
	})
	if sess.CompactionSummary != nil && *sess.CompactionSummary != "" {
		systemBlocks = append(systemBlocks, provider.SystemBlock{
			Text: *sess.CompactionSummary,
		})
	}

	// Tool definitions.
	var toolDefs []provider.ToolDef
	if a.tools != nil {
		toolDefs = a.tools.Definitions()
	}

	model := a.Model()
	if model == "" {
		model = sess.Model
	}

	return &provider.Request{
		Model:    model,
		System:   systemBlocks,
		Messages: providerMsgs,
		Tools:    toolDefs,
		Effort:   a.effort,
	}, nil
}

// --- Helpers ----------------------------------------------------------

// contentBlockJSON is the JSON representation of a ContentBlock for store
// persistence. Field names use snake_case to match the store's FTS extractor.
type contentBlockJSON struct {
	Type              string          `json:"type"`
	Text              string          `json:"text,omitempty"`
	ToolCallID        string          `json:"tool_call_id,omitempty"`
	ToolName          string          `json:"tool_name,omitempty"`
	ToolInput         json.RawMessage `json:"tool_input,omitempty"`
	ToolResult        string          `json:"tool_result,omitempty"`
	ToolIsError       bool            `json:"tool_is_error,omitempty"`
	Thinking          string          `json:"thinking,omitempty"`
	ThinkingSignature string          `json:"thinking_signature,omitempty"`
	Redacted          bool            `json:"redacted,omitempty"`
}

// contentBlocksToJSON serializes a slice of ContentBlocks into a JSON string
// suitable for the store's content column. An empty slice produces "[]".
func contentBlocksToJSON(blocks []provider.ContentBlock) (string, error) {
	if blocks == nil {
		blocks = []provider.ContentBlock{}
	}
	out := make([]contentBlockJSON, len(blocks))
	for i, b := range blocks {
		out[i] = contentBlockJSON{
			Type:              b.Type,
			Text:              b.Text,
			ToolCallID:        b.ToolCallID,
			ToolName:          b.ToolName,
			ToolInput:         b.ToolInput,
			ToolResult:        b.ToolResult,
			ToolIsError:       b.ToolIsError,
			Thinking:          b.Thinking,
			ThinkingSignature: b.ThinkingSignature,
			Redacted:          b.Redacted,
		}
	}
	data, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// messageToProvider converts a store.Message to a provider.Message by parsing
// the content JSON into ContentBlocks. If the content is not valid JSON it
// falls back to a single text block with the raw content.
func messageToProvider(msg store.Message) (provider.Message, error) {
	var blocks []contentBlockJSON
	if err := json.Unmarshal([]byte(msg.Content), &blocks); err != nil {
		return provider.Message{
			Role:    msg.Role,
			Content: []provider.ContentBlock{{Type: "text", Text: msg.Content}},
		}, nil
	}
	content := make([]provider.ContentBlock, len(blocks))
	for i, b := range blocks {
		content[i] = provider.ContentBlock{
			Type:              b.Type,
			Text:              b.Text,
			ToolCallID:        b.ToolCallID,
			ToolName:          b.ToolName,
			ToolInput:         b.ToolInput,
			ToolResult:        b.ToolResult,
			ToolIsError:       b.ToolIsError,
			Thinking:          b.Thinking,
			ThinkingSignature: b.ThinkingSignature,
			Redacted:          b.Redacted,
		}
	}
	return provider.Message{
		Role:    msg.Role,
		Content: content,
	}, nil
}

// buildAssistantBlocks assembles the content blocks for the assistant message
// from the streamed text and collected tool calls.
func buildAssistantBlocks(thinking, thinkingSig string, redacted []provider.ContentBlock, text string, toolCalls []provider.ToolCall) []provider.ContentBlock {
	var blocks []provider.ContentBlock
	// Thinking blocks must precede text and tool_use (Anthropic ordering).
	blocks = append(blocks, redacted...)
	if thinking != "" {
		blocks = append(blocks, provider.ContentBlock{
			Type: "thinking", Thinking: thinking, ThinkingSignature: thinkingSig,
		})
	}
	if text != "" {
		blocks = append(blocks, provider.ContentBlock{Type: "text", Text: text})
	}
	for _, tc := range toolCalls {
		input := tc.Input
		if len(input) == 0 {
			input = json.RawMessage("{}")
		}
		blocks = append(blocks, provider.ContentBlock{
			Type:       "tool_use",
			ToolCallID: tc.ID,
			ToolName:   tc.Name,
			ToolInput:  input,
		})
	}
	return blocks
}

// updateToolCall updates a tool call in the list by matching ID. If the ID
// is empty or no match is found, the last entry is updated as a fallback.
func (a *Agent) updateToolCall(toolCalls []provider.ToolCall, updated *provider.ToolCall) {
	if updated == nil || len(toolCalls) == 0 {
		return
	}
	if updated.ID != "" {
		for i := range toolCalls {
			if toolCalls[i].ID == updated.ID {
				toolCalls[i] = *updated
				return
			}
		}
	}
	// Fallback: update the last entry.
	toolCalls[len(toolCalls)-1] = *updated
}

// recordAPICall records a row in the api_calls table with exact usage and
// computed cost, and returns the generated ID.
func (a *Agent) recordAPICall(usage *provider.Usage) (string, error) {
	model := a.currentModel()
	providerID := a.provider.ID()

	cacheRead, cacheWrite := usage.CacheReadTokens, usage.CacheWriteTokens

	cost := a.store.ComputeCost(providerID, model,
		usage.InputTokens, usage.OutputTokens, cacheRead, cacheWrite)

	seq := a.nextAPICallSeq()

	call := &store.APICall{
		SessionID:          a.sessionID,
		Seq:                seq,
		Model:              model,
		InputTokens:        usage.InputTokens,
		InputTokensUnknown: usage.InputTokensUnknown,
		OutputTokens:       usage.OutputTokens,
		CacheReadTokens:    cacheRead,
		CacheWriteTokens:   cacheWrite,
		Cost:               cost,
	}
	if err := a.store.RecordAPICall(call); err != nil {
		return "", err
	}
	return call.ID, nil
}

// nextAPICallSeq returns the next sequence number for api_calls in this
// session (max(seq) + 1, or 1 if no rows yet).
func (a *Agent) nextAPICallSeq() int {
	var ms int
	row := a.store.DB().QueryRow(
		`SELECT COALESCE(MAX(seq), 0) FROM api_calls WHERE session_id = ?`,
		a.sessionID)
	if err := row.Scan(&ms); err != nil {
		return 1
	}
	return ms + 1
}

// currentModel returns the model from the session, falling back to config.
func (a *Agent) currentModel() string {
	sess, err := a.store.GetSession(a.sessionID)
	if err != nil || sess == nil {
		switch a.provider.ID() {
		case "ollama":
			return a.config.Ollama.Model
		case "anthropic":
			return a.config.Anthropic.Model
		case "xai":
			return a.config.XAI.Model
		}
		return ""
	}
	return sess.Model
}

// sendEvent sends an OutputEvent to the output channel. If the channel is nil
// (no TUI attached), the event is silently dropped.
func (a *Agent) sendEvent(ev OutputEvent) {
	if a.outputChan != nil {
		a.outputChan <- ev
	}
}
