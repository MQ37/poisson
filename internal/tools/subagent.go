package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/store"
	"github.com/mq37/poisson/internal/subagent"
)

// removeDBFiles deletes a SQLite database and its WAL/SHM sidecars.
func removeDBFiles(path string) {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(path + suffix)
	}
}

// SubagentApproval handles bash approval requests forwarded from the child.
// risk is the child's assessed level (low/medium/high); empty means unknown.
// reason is an optional human-supplied explanation when denied.
type SubagentApproval func(command, description, workdir, agentName, risk string) (allowed bool, reason string)

// SubagentTool spawns a one-shot child Poisson process for isolated work. The
// child's conversation is ephemeral (a throwaway temp DB) and its internal
// steps never enter the parent's conversation — only the final result is
// returned to the calling model. The parent UI shows a compact widget derived
// from the tool_start / tool_result events, not the child's steps.
type SubagentTool struct {
	cwd             string
	providerFn      func() string
	modelFn         func() string
	effortFn        func() string
	skillsEnabledFn func() bool // nil, or unset result, means "enabled" (matches the main session's default)
	approvalFn      SubagentApproval

	// live tracks currently-running child processes so ExpediteAll can nudge
	// them to wrap up. Guarded by liveMu; touched from parallel tool goroutines
	// (register/unregister) and the TUI goroutine (ExpediteAll).
	liveMu sync.Mutex
	live   map[*subagent.ChildProcess]struct{}

	// progressFn reports a live turn-count + context-usage update for the
	// running widget, correlated via the tool_call ID attached to Execute's
	// context. status is normally "" (ordinary progress); when the child is
	// mid-network-retry it carries a short human-readable status ("connection
	// lost: ... — reconnecting…" / "reconnected — resuming") for the widget
	// to show in place of the turn/context line. tokensPerSec is the child's
	// own last-reported inference speed (0 if none reported yet).
	progressFn func(toolCallID string, turns, contextTokens, contextWindow int, tokensPerSec float64, status string)

	// usageFn records a finished (or partially finished) subagent's
	// accumulated token usage as a "subagent" api_calls row on the parent
	// session, so the spend counts toward the parent's /cost and status-bar
	// total instead of vanishing with the child's ephemeral, throwaway DB.
	// Returns the computed cost. nil means no recorder wired (e.g. tests
	// that don't care) — Execute treats that as "nothing to record".
	usageFn func(providerID, model string, usage *provider.Usage) (float64, error)
}

// NewSubagentTool creates a subagent tool.
func NewSubagentTool(cwd string, approvalFn SubagentApproval) *SubagentTool {
	return &SubagentTool{
		cwd:        cwd,
		approvalFn: approvalFn,
		live:       make(map[*subagent.ChildProcess]struct{}),
	}
}

func (t *SubagentTool) trackLive(c *subagent.ChildProcess) {
	t.liveMu.Lock()
	t.live[c] = struct{}{}
	t.liveMu.Unlock()
}

func (t *SubagentTool) untrackLive(c *subagent.ChildProcess) {
	t.liveMu.Lock()
	delete(t.live, c)
	t.liveMu.Unlock()
}

// ExpediteAll forwards a "finish now" nudge to every live subagent child,
// returning how many were signalled. Safe to call from any goroutine.
func (t *SubagentTool) ExpediteAll() int {
	t.liveMu.Lock()
	defer t.liveMu.Unlock()
	n := 0
	for c := range t.live {
		if c.SendExpedite() == nil {
			n++
		}
	}
	return n
}

// SetRuntime supplies live provider/model/effort resolvers (called at spawn time).
func (t *SubagentTool) SetRuntime(providerFn, modelFn, effortFn func() string) {
	t.providerFn = providerFn
	t.modelFn = modelFn
	t.effortFn = effortFn
}

// SetSkillsEnabledFn supplies a live resolver for whether the main session
// has skills enabled, so a spawned subagent mirrors that (SetSkills(false)
// / --no-skills means the whole tree, not just the parent, goes without).
func (t *SubagentTool) SetSkillsEnabledFn(fn func() bool) {
	t.skillsEnabledFn = fn
}

// SetProgressFn supplies the live turn-count + context-usage progress
// callback (called from Execute's goroutine as the child reports each new turn).
func (t *SubagentTool) SetProgressFn(fn func(toolCallID string, turns, contextTokens, contextWindow int, tokensPerSec float64, status string)) {
	t.progressFn = fn
}

// SetUsageFn supplies the callback that rolls a finished subagent's token
// usage into the parent session's cost (see usageFn's doc comment).
func (t *SubagentTool) SetUsageFn(fn func(providerID, model string, usage *provider.Usage) (float64, error)) {
	t.usageFn = fn
}

func (t *SubagentTool) Name() string { return "subagent" }

func (t *SubagentTool) Description() string {
	return "Spawn a one-shot child Poisson agent to complete a specific task. The child has every tool you do (read, write, edit, bash, web_search, web_ask, recall) except the ability to spawn further subagents. Use when you need focused work isolated from the main session. The child returns its final output when done. It cannot ask questions — give it a complete, self-contained task."
}

func (t *SubagentTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"task": {"type": "string", "description": "Complete, self-contained task for the subagent. Include context, file paths, and expected output format."},
			"name": {"type": "string", "description": "Display name for the subagent. If omitted, a name is chosen automatically."}
		},
		"required": ["task"]
	}`)
}

func (t *SubagentTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	var params struct {
		Task string `json:"task"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return ToolResult{Error: "invalid input: " + err.Error()}, nil
	}
	if params.Task == "" {
		return ToolResult{Error: "task is required"}, nil
	}

	agentName := params.Name
	if agentName == "" {
		agentName = "subagent"
	}

	// A subagent must always run the same provider + model as the main
	// session — never a silent fallback to some other model, which would
	// change cost/behavior/quality without the user ever choosing it. If the
	// resolvers aren't wired or the main session can't report a provider/model
	// right now, that's a real configuration problem: fail loudly instead of
	// guessing.
	if t.providerFn == nil || t.modelFn == nil {
		return ToolResult{Error: "subagent runtime not configured: provider/model resolver missing"}, nil
	}
	prov := t.providerFn()
	if prov == "" {
		return ToolResult{Error: "subagent cannot start: main session reported no provider"}, nil
	}
	model := t.modelFn()
	if model == "" {
		return ToolResult{Error: "subagent cannot start: main session reported no model"}, nil
	}
	childSessionID := store.NewSubagentID()
	effort := ""
	if t.effortFn != nil {
		effort = t.effortFn()
	}

	// The child conversation is ephemeral: it lives in a throwaway DB under the
	// OS temp dir and is deleted when the subagent finishes, so nothing is
	// persisted to the parent's DB (same policy as /btw).
	dbPath := filepath.Join(os.TempDir(), "poisson-"+childSessionID+".db")
	defer removeDBFiles(dbPath)

	child, err := subagent.Spawn(subagent.SpawnInput{
		Task:      params.Task,
		Cwd:       t.cwd,
		SessionID: childSessionID,
		Name:      agentName,
		Provider:  prov,
		Model:     model,
		Effort:    effort,
		NoSkills:  t.skillsEnabledFn != nil && !t.skillsEnabledFn(),
		DBPath:    dbPath,
	})
	if err != nil {
		return ToolResult{Error: "failed to spawn subagent: " + err.Error()}, nil
	}
	defer child.Reap()
	t.trackLive(child)
	defer t.untrackLive(child)

	var output strings.Builder
	var toolCount, turns, contextTokens, contextWindow int
	var tokensPerSec float64
	var success bool
	var childErr string
	toolCallID, hasToolCallID := ToolCallIDFromContext(ctx)
	reportProgress := func(status string) {
		if hasToolCallID && t.progressFn != nil {
			t.progressFn(toolCallID, turns, contextTokens, contextWindow, tokensPerSec, status)
		}
	}

	// lastUsage holds the child's most recently reported cumulative token
	// usage (relayed on every "tool" tick and on "done", see ChildEvent.Usage)
	// and recordedCost/recorded track whether it's already been rolled into
	// the parent session. Recording explicitly happens at the "done" event so
	// the cost can be folded into the returned summary text; the deferred
	// call below is a guarded fallback that fires the same recording exactly
	// once for every OTHER return path in this function (ctx cancelled,
	// approval-send failure, etc.) — a subagent killed mid-run by a cancelled
	// parent turn still gets partial credit for whatever it had already
	// spent as of its last progress tick.
	var lastUsage provider.Usage
	var usageSeen, recorded bool
	var recordedCost float64
	recordUsage := func() {
		if recorded || !usageSeen || t.usageFn == nil {
			return
		}
		if lastUsage.InputTokens == 0 && lastUsage.OutputTokens == 0 &&
			lastUsage.CacheReadTokens == 0 && lastUsage.CacheWriteTokens == 0 {
			return // nothing billed yet (e.g. cancelled before the child's first turn completed)
		}
		recorded = true
		cost, err := t.usageFn(prov, model, &lastUsage)
		if err != nil {
			log.Printf("warning: record subagent usage: %v", err)
			return
		}
		recordedCost = cost
	}
	defer recordUsage()

	for {
		if err := ctx.Err(); err != nil {
			return ToolResult{Content: output.String(), Error: "subagent cancelled"}, nil
		}

		type readResult struct {
			ev  *subagent.ChildEvent
			err error
		}
		readCh := make(chan readResult, 1)
		go func() {
			ev, readErr := child.ReadEvent()
			readCh <- readResult{ev: ev, err: readErr}
		}()

		select {
		case <-ctx.Done():
			return ToolResult{Content: output.String(), Error: "subagent cancelled"}, nil
		case res := <-readCh:
			if res.err != nil {
				if ctx.Err() != nil {
					return ToolResult{Content: output.String(), Error: "subagent cancelled"}, nil
				}
				if res.err.Error() != "" && childErr == "" {
					childErr = res.err.Error()
				}
				goto done
			}
			ev := res.ev
			if ev == nil {
				continue
			}

			// The child's internal steps (text, tool calls, tool results) are
			// intentionally NOT forwarded to the parent UI: only the final result
			// is returned to the calling model, and the parent shows a compact
			// widget. We still accumulate text + count tools for the summary.
			switch ev.Type {
			case "text":
				output.WriteString(ev.Text)

			case "tool":
				toolCount++
				if ev.Turns > 0 {
					turns = ev.Turns
					contextTokens, contextWindow = ev.ContextTokens, ev.ContextWindow
					// A real progress update means the child is actively working
					// again, so this also clears any "reconnecting" status the
					// widget was showing.
					reportProgress("")
				}
				if ev.Usage != nil {
					lastUsage, usageSeen = *ev.Usage, true
				}

			case "retrying":
				// Relayed from the child's own network-retry notice (see
				// agent.OutputRetrying) so the widget can show "reconnecting"
				// instead of freezing on stale turn/context numbers with no
				// explanation while the child's connection recovers.
				reportProgress(ev.Text)

			case "speed":
				// Relayed from the child's own agent.OutputInferenceSpeed —
				// only sent by the child when it has an actual reading (see
				// forwardChildEvents), so this is always a real update.
				tokensPerSec = ev.TokensPerSec
				reportProgress("")

			case "tool_result":

			case "approval_request":
				if ctx.Err() != nil {
					goto done
				}
				approved, reason := false, ""
				if t.approvalFn != nil {
					approved, reason = t.approvalFn(ev.Command, ev.Description, ev.Cwd, ev.Agent, ev.Risk)
				}
				if err := child.SendApprovalSafe(approved, reason); err != nil {
					childErr = "approval response failed: " + err.Error()
					goto done
				}

			case "done":
				success = ev.Success
				if ev.Turns > 0 {
					turns = ev.Turns
					contextTokens, contextWindow = ev.ContextTokens, ev.ContextWindow
					reportProgress("") // final count, before the card flips to done
				}
				if ev.Usage != nil {
					lastUsage, usageSeen = *ev.Usage, true
				}
				if ev.Error != "" {
					childErr = ev.Error
				}
				goto done

			case "error":
				childErr = ev.Error
				goto done
			}
		}
	}

done:
	// Record now (rather than leaving it entirely to the deferred fallback)
	// so the cost, once known, can be folded into the summary text below.
	// recordUsage() is idempotent (guarded by `recorded`), so the deferred
	// call is then a no-op here and only does real work on the return paths
	// above that jump straight past this label.
	recordUsage()
	result := output.String()
	result += fmt.Sprintf("\n\n---\nSubagent finished. %d tool calls, %d turns.", toolCount, turns)
	if recorded {
		result += fmt.Sprintf(" Cost: $%.4f.", recordedCost)
	}
	if childErr != "" {
		result += "\nError: " + childErr
		return ToolResult{Content: result, Error: childErr}, nil
	}
	if !success {
		result += " (subagent reported failure)"
	}
	return ToolResult{Content: result}, nil
}
