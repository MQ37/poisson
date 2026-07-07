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
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"poisson/internal/config"
	"poisson/internal/pricing"
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

	CompactionTokensBefore int // compacted
	CompactionTokensAfter  int // compacted
	ThinkingRedacted       bool // thinking (opaque redacted block)
}

// Agent runs the turn loop for a single session.
type Agent struct {
	store      *store.Store
	provider   provider.Provider
	tools      *tools.Registry
	config     *config.Config
	sessionID  string
	outputChan chan OutputEvent
	approvalFn func(ctx context.Context, command, description, workdir string) bool
	model      string
	effort     string

	// session tool counters for the status bar (reset on SwitchSession).
	sessionToolCalls  int
	sessionToolErrors int

	// sysTokensEstimate caches the estimated token size of the system prompt
	// (base instructions + AGENTS.md + tool-name list + skills) plus the tool
	// definition schemas. buildRequest recomputes it each turn from the exact
	// text it sends; the status bar reads it (from either goroutine) via atomic
	// so the context counter reflects the whole prompt, not just messages.
	sysTokensEstimate atomic.Int64

	// expedite is set (in subagent/child mode) when the parent forwards the
	// user's Ctrl+G "finish now" nudge. At the next micro-turn boundary the turn
	// loop appends expediteNudge to the last tool result so the model wraps up
	// with partial results. Written by the child's stdin-reader goroutine, read
	// by the turn-loop goroutine — hence atomic. Never set in the main agent.
	expedite atomic.Bool

	// compactBackoffUntil suppresses auto-compaction retries after a failure.
	compactBackoffUntil time.Time

	skillsEnabled bool
	skills        []skills.Skill

	// contextMu guards loadedContextDirs.
	contextMu sync.Mutex
	// loadedContextDirs records directories whose AGENTS.md has been injected
	// into the conversation this epoch (a file was worked on there). Each is
	// injected once; the set is reset on compaction and session switch so the
	// files are re-loaded afterwards.
	loadedContextDirs map[string]bool
}

// NewAgent creates an Agent ready to process prompts for the given session.
func NewAgent(
	s *store.Store,
	p provider.Provider,
	t *tools.Registry,
	cfg *config.Config,
	sessionID string,
	outputChan chan OutputEvent,
	approvalFn func(ctx context.Context, command, description, workdir string) bool,
) *Agent {
	model := defaultModel(p, cfg)
	a := &Agent{
		store:      s,
		provider:   p,
		tools:      t,
		config:     cfg,
		sessionID:  sessionID,
		outputChan: outputChan,
		approvalFn: approvalFn,
		model:      model,
		effort:     effectiveEffort(initialEffort(cfg), p.ID(), model),

		loadedContextDirs: map[string]bool{},
	}
	return a
}

// initialEffort resolves the starting reasoning effort from config, falling back
// to the built-in default so the status bar always shows a level.
func initialEffort(cfg *config.Config) string {
	if cfg != nil && cfg.Effort != "" {
		return cfg.Effort
	}
	return config.DefaultEffort
}

// effectiveEffort validates effort against the model's supported levels. If the
// model is known and doesn't support the requested level, the first supported
// level is used instead. Unknown models keep the effort (the provider decides).
func effectiveEffort(effort, providerID, model string) string {
	if effort == "" {
		return ""
	}
	s, ok := provider.GetModelSettings(providerID, model)
	if !ok {
		return effort // unknown model — keep it, provider will handle
	}
	if !s.SupportsEffort {
		return "" // known model that doesn't support effort
	}
	if len(s.EffortLevels) == 0 {
		return effort // supports effort but no specific levels listed
	}
	for _, lvl := range s.EffortLevels {
		if lvl == effort {
			return effort
		}
	}
	return s.EffortLevels[0] // not in supported list — use first supported
}

// --- Session management accessors (for TUI slash commands) ---

// Store returns the underlying store (for session/message queries).
func (a *Agent) Store() *store.Store { return a.store }

// SessionID returns the current session ID.
func (a *Agent) SessionID() string { return a.sessionID }

// SessionToolStats returns the tool-call and tool-error counts for the current
// session run. Reset on session switch (see SwitchSession).
func (a *Agent) SessionToolStats() (calls, errors int) {
	return a.sessionToolCalls, a.sessionToolErrors
}

// SwitchSession changes the active session.
func (a *Agent) SwitchSession(sessionID string) {
	a.sessionID = sessionID
	a.sessionToolCalls = 0
	a.sessionToolErrors = 0
	a.effort = effectiveEffort(initialEffort(a.config), a.provider.ID(), a.model)
	a.resetContextTracker()
}

// resetContextTracker forgets which directories' AGENTS.md have been injected,
// so they load again. Called on session switch and after compaction.
func (a *Agent) resetContextTracker() {
	a.contextMu.Lock()
	a.loadedContextDirs = map[string]bool{}
	a.contextMu.Unlock()
}

// cwd resolves the working directory for the active session.
func (a *Agent) cwd() string {
	if sess, err := a.store.GetSession(a.sessionID); err == nil && sess != nil && sess.Cwd != "" {
		return sess.Cwd
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

// SetProvider swaps the provider and persists it on the active session. A
// session that isn't persisted yet is not an error (nothing to update); a
// failed write is returned so the caller can surface it.
func (a *Agent) SetProvider(p provider.Provider) error {
	a.provider = p
	a.effort = effectiveEffort(a.effort, p.ID(), a.model)
	sess, err := a.store.GetSession(a.sessionID)
	if err != nil {
		return nil
	}
	sess.Provider = p.ID()
	sess.UpdatedAt = time.Now().Unix()
	return a.store.UpdateSession(sess)
}

// SetModel updates the session's model name and persists it. See SetProvider
// for the error semantics.
func (a *Agent) SetModel(model string) error {
	a.model = model
	a.effort = effectiveEffort(a.effort, a.provider.ID(), model)
	sess, err := a.store.GetSession(a.sessionID)
	if err != nil {
		return nil
	}
	sess.Model = model
	sess.UpdatedAt = time.Now().Unix()
	return a.store.UpdateSession(sess)
}

// SetConfig swaps the config (for /reload).
func (a *Agent) SetConfig(cfg *config.Config) {
	a.config = cfg
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
	if a.provider.ID() == "ollama" && tools.IsOllamaReachable(a.config) {
		a.tools.Register(tools.NewFetchTool(a.config.Ollama.BaseURL))
	} else {
		a.tools.Unregister("fetch")
	}
}

// Provider returns the current provider.
func (a *Agent) Provider() provider.Provider { return a.provider }

// Config returns the current config.
func (a *Agent) Config() *config.Config { return a.config }

// contextInjectionForFile returns the AGENTS.md/CLAUDE.md content to append to a
// tool result when a file was worked on (read/edit/write), loading each
// applicable file at most once per epoch. When cwd is an ancestor of the file's
// directory, the whole chain from cwd down to that directory is considered;
// otherwise only the file's own directory is. Files already carried by the
// system prompt (sysPaths: global + cwd) are never re-injected.
func (a *Agent) contextInjectionForFile(cwd, toolName string, input json.RawMessage, sysPaths map[string]bool) string {
	switch toolName {
	case "read", "edit", "write":
	default:
		return ""
	}
	var in struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(input, &in) != nil || in.Path == "" {
		return ""
	}
	p := in.Path
	if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	dirs := project.ContextDirsForFile(cwd, filepath.Dir(p))

	var out strings.Builder
	a.contextMu.Lock()
	defer a.contextMu.Unlock()
	if a.loadedContextDirs == nil {
		a.loadedContextDirs = map[string]bool{}
	}
	for _, d := range dirs {
		if a.loadedContextDirs[d] {
			continue
		}
		cf := project.ContextFileInDir(d)
		if cf == nil || sysPaths[cf.Path] {
			continue
		}
		a.loadedContextDirs[d] = true
		out.WriteString("\n\n<project_instructions path=\"")
		out.WriteString(cf.Path)
		out.WriteString("\">\n")
		out.WriteString(cf.Content)
		out.WriteString("\n</project_instructions>")
	}
	return out.String()
}

// systemPromptContextPaths returns the set of AGENTS.md/CLAUDE.md paths carried
// by the system prompt (global + cwd), which must never be re-injected.
func (a *Agent) systemPromptContextPaths(cwd string) map[string]bool {
	paths := map[string]bool{}
	for _, cf := range project.LoadProjectContextFiles(cwd, config.ConfigDir(), nil) {
		paths[cf.Path] = true
	}
	return paths
}

// LoadedContextFiles returns every AGENTS.md/CLAUDE.md currently in the
// session's context: the system-prompt ones (global + cwd) plus each directory
// whose file has been injected this epoch. Used by /status.
func (a *Agent) LoadedContextFiles() []project.ContextFile {
	cwd := a.cwd()
	a.contextMu.Lock()
	dirs := make([]string, 0, len(a.loadedContextDirs))
	for d := range a.loadedContextDirs {
		dirs = append(dirs, d)
	}
	a.contextMu.Unlock()
	return project.LoadProjectContextFiles(cwd, config.ConfigDir(), dirs)
}

// ToolNames returns the sorted names of the currently registered tools.
func (a *Agent) ToolNames() []string {
	if a.tools == nil {
		return nil
	}
	defs := a.tools.Definitions()
	names := make([]string, 0, len(defs))
	for _, td := range defs {
		names = append(names, td.Name)
	}
	sort.Strings(names)
	return names
}

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
	case "openai":
		return cfg.OpenAI.Model
	default:
		return ""
	}
}

// Effort returns the current thinking effort level.
func (a *Agent) Effort() string { return a.effort }

// Expedite marks this agent to wrap up early. A subagent child sets it when the
// parent forwards the user's Ctrl+G nudge; the turn loop then injects a
// finish-now message at the next micro-turn boundary. No-op in the main agent.
func (a *Agent) Expedite() { a.expedite.Store(true) }

// ExpediteSubagents forwards the user's "finish now" nudge to every running
// subagent child and returns how many were signalled. The main agent's own
// turn is left untouched. Used by the TUI Ctrl+G handler.
func (a *Agent) ExpediteSubagents() int {
	if a.tools == nil {
		return 0
	}
	t, ok := a.tools.Get("subagent")
	if !ok {
		return 0
	}
	st, ok := t.(*tools.SubagentTool)
	if !ok {
		return 0
	}
	return st.ExpediteAll()
}

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

// ImageAttachment is an already-processed image (downscaled, on disk) to send
// with a user message.
type ImageAttachment struct {
	Path      string
	MediaType string
}

// Prompt appends the user message to the store and runs the turn loop.
func (a *Agent) Prompt(userInput string) error {
	return a.PromptWithContext(context.Background(), userInput)
}

// PromptWithContext is Prompt with cancellation support. Any images are sent as
// image content blocks alongside the text in the user message.
func (a *Agent) PromptWithContext(ctx context.Context, userInput string, images ...ImageAttachment) error {
	if err := a.EnsureSession(); err != nil {
		a.sendEvent(OutputEvent{Type: OutputError, Text: fmt.Sprintf("Session error: %v", err)})
		a.sendEvent(OutputEvent{Type: OutputDone})
		return fmt.Errorf("ensure session: %w", err)
	}

	// INGEST: append user message (images first, then the text).
	var blocks []provider.ContentBlock
	for _, im := range images {
		if im.Path == "" {
			continue
		}
		mt := im.MediaType
		if mt == "" {
			mt = "image/png"
		}
		blocks = append(blocks, provider.ContentBlock{Type: "image", MediaType: mt, ImagePath: im.Path})
	}
	if userInput != "" || len(blocks) == 0 {
		blocks = append(blocks, provider.ContentBlock{Type: "text", Text: userInput})
	}
	content, err := contentBlocksToJSON(blocks)
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
		// Keep the conversation visible — just stop generation. runTurn already
		// returns before storing the final (incomplete) assistant message, so
		// there are no orphaned tool_use blocks. Previous tool iterations (if
		// any) have complete tool_use+result pairs.
	}
	return err
}

// expediteNudge is appended to the last tool result when the user asks a
// subagent to finish early (Ctrl+G). It rides inside the tool-result (user)
// turn, so it never creates consecutive user messages some providers reject.
// A model occasionally returns a complete-but-empty response (no text,
// thinking, or tool calls) — a transient provider glitch, seen most with
// Anthropic. runTurn retries the same request up to maxEmptyResponseRetries
// times (Nth retry waits N × emptyResponseBackoff) before surfacing an error.
const maxEmptyResponseRetries = 3

// emptyResponseBackoff is a var so tests can shorten it.
var emptyResponseBackoff = 500 * time.Millisecond

const expediteNudge = "\n\n[User interjection] The user needs results now and has asked you to wrap up immediately. Stop starting new work: summarize what you have accomplished so far — partial results are fine — and finish this turn without any further tool calls."

// runTurn executes the turn loop: build → stream → collect tools → dispatch →
// append results → check compaction → repeat until no tool calls.
func (a *Agent) runTurn(ctx context.Context) error {
	emptyAttempts := 0
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
				a.sendEvent(OutputEvent{Type: OutputThinking, ThinkingRedacted: true})

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
		if len(assistantBlocks) == 0 {
			// Model returned nothing (no text, thinking, or tool calls). This is
			// a transient provider glitch (notably Anthropic), so retry the same
			// request a few times before giving up — erroring out here would
			// strand the turn and force the user to re-prompt, leaving two
			// consecutive user messages in history. Don't persist the empty
			// message: an empty content array is a provider 400 next turn.
			if emptyAttempts < maxEmptyResponseRetries {
				emptyAttempts++
				select {
				case <-ctx.Done():
					a.sendEvent(OutputEvent{Type: OutputDone})
					return ctx.Err()
				case <-time.After(time.Duration(emptyAttempts) * emptyResponseBackoff):
				}
				continue
			}
			a.sendEvent(OutputEvent{Type: OutputError, Text: "model returned no content"})
			a.sendEvent(OutputEvent{Type: OutputDone})
			return fmt.Errorf("model returned empty response")
		}
		emptyAttempts = 0
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

		// Persist tool_result messages even if the context was cancelled — the
		// results are already computed and leaving orphaned tool_use blocks
		// without results would cause a provider 400 on the next request.
		turnCwd := a.cwd()
		sysCtxPaths := a.systemPromptContextPaths(turnCwd)
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
				// Attach any not-yet-loaded AGENTS.md for the file's directory
				// (and the cwd→dir chain) so the model gets its project rules.
				toolBlock.ToolResult += a.contextInjectionForFile(
					turnCwd, toolCalls[i].Name, toolCalls[i].Input, sysCtxPaths)
			}
			// If the user asked us to wrap up (Ctrl+G — subagents only), append the
			// nudge to the last tool result so the model sees it on the next turn.
			if i == len(results)-1 && a.expedite.Swap(false) {
				toolBlock.ToolResult += expediteNudge
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

			a.sessionToolCalls++
			if result.Error != "" {
				a.sessionToolErrors++
			}

		}

		a.UpdateStatus()

		if err := ctx.Err(); err != nil {
			a.sendEvent(OutputEvent{Type: OutputDone})
			return err
		}

		if a.shouldCompact() {
			if err := a.compact(ctx, true, true); err != nil {
				if !errors.Is(err, ErrNothingToCompact) {
					log.Printf("warning: auto-compaction failed: %v", err)
				}
				a.compactBackoffUntil = time.Now().Add(90 * time.Second)
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

	// System prompt carries only the always-relevant context: the global
	// ~/.poisson AGENTS.md and the cwd's own. Directory-specific AGENTS.md for
	// files the agent works on are injected into the conversation on demand (see
	// contextInjectionForFile), not the system prompt.
	contextFiles := project.LoadProjectContextFiles(sess.Cwd, config.ConfigDir(), nil)
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

	// Cache the system-side token estimate for the status bar: the system prompt
	// plus each tool's serialized schema. Messages and the compaction summary are
	// counted separately by estimateMessagesTokens, so they are excluded here.
	sysEst := a.EstimateTokens(sysPrompt)
	for _, td := range toolDefs {
		if b, err := json.Marshal(td); err == nil {
			sysEst += a.EstimateTokens(string(b))
		}
	}
	a.sysTokensEstimate.Store(int64(sysEst))

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
		CacheKey: a.sessionID, // stable per conversation → OpenAI prompt caching
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
	MediaType         string          `json:"media_type,omitempty"`
	ImagePath         string          `json:"image_path,omitempty"`
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
			MediaType:         b.MediaType,
			ImagePath:         b.ImagePath,
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
			MediaType:         b.MediaType,
			ImagePath:         b.ImagePath,
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

func (a *Agent) computeCost(providerID, model string, input, output, cacheRead, cacheWrite int) float64 {
	return pricing.ComputeCost(a.config, providerID, model, input, output, cacheRead, cacheWrite)
}

// recordAPICall records a row in the api_calls table with exact usage and
// computed cost, and returns the generated ID.
func (a *Agent) recordAPICall(usage *provider.Usage) (string, error) {
	return a.recordAPICallFlags(usage, false, "")
}

func (a *Agent) recordCompactionAPICall(model string, usage *provider.Usage) error {
	_, err := a.recordAPICallFlags(usage, true, model)
	return err
}

func (a *Agent) recordAPICallFlags(usage *provider.Usage, isCompaction bool, modelOverride string) (string, error) {
	model := modelOverride
	if model == "" {
		model = a.currentModel()
	}
	providerID := a.provider.ID()

	cacheRead, cacheWrite := usage.CacheReadTokens, usage.CacheWriteTokens

	cost := a.computeCost(providerID, model,
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
		IsCompaction:       isCompaction,
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
		case "openai":
			return a.config.OpenAI.Model
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
