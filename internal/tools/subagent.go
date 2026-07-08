package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"poisson/internal/store"
	"poisson/internal/subagent"
)

// removeDBFiles deletes a SQLite database and its WAL/SHM sidecars.
func removeDBFiles(path string) {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(path + suffix)
	}
}

// SubagentApproval handles bash approval requests forwarded from the child.
// risk is the child's assessed level (low/medium/high); empty means unknown.
type SubagentApproval func(command, description, workdir, agentName, risk string) bool

// SubagentTool spawns a one-shot child Poisson process for isolated work. The
// child's conversation is ephemeral (a throwaway temp DB) and its internal
// steps never enter the parent's conversation — only the final result is
// returned to the calling model. The parent UI shows a compact widget derived
// from the tool_start / tool_result events, not the child's steps.
type SubagentTool struct {
	cwd        string
	providerFn func() string
	modelFn    func() string
	effortFn   func() string
	approvalFn SubagentApproval

	// live tracks currently-running child processes so ExpediteAll can nudge
	// them to wrap up. Guarded by liveMu; touched from parallel tool goroutines
	// (register/unregister) and the TUI goroutine (ExpediteAll).
	liveMu sync.Mutex
	live   map[*subagent.ChildProcess]struct{}

	// progressFn reports a live turn-count + context-usage update for the
	// running widget, correlated via the tool_call ID attached to Execute's
	// context.
	progressFn func(toolCallID string, turns, contextTokens, contextWindow int)
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

// SetProgressFn supplies the live turn-count + context-usage progress
// callback (called from Execute's goroutine as the child reports each new turn).
func (t *SubagentTool) SetProgressFn(fn func(toolCallID string, turns, contextTokens, contextWindow int)) {
	t.progressFn = fn
}

func (t *SubagentTool) Name() string { return "subagent" }

func (t *SubagentTool) Description() string {
	return "Spawn a one-shot child Poisson agent to complete a specific task. The child has every tool you do (read, write, edit, bash, search, ls, glob, exa_search, recall) except the ability to spawn further subagents. Use when you need focused work isolated from the main session. The child returns its final output when done. It cannot ask questions — give it a complete, self-contained task."
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

	childSessionID := store.NewSubagentID()
	prov, model := "ollama", "glm-5.2:cloud"
	if t.providerFn != nil {
		if p := t.providerFn(); p != "" {
			prov = p
		}
	}
	if t.modelFn != nil {
		if m := t.modelFn(); m != "" {
			model = m
		}
	}
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
	var success bool
	var childErr string
	toolCallID, hasToolCallID := ToolCallIDFromContext(ctx)
	reportProgress := func() {
		if hasToolCallID && t.progressFn != nil {
			t.progressFn(toolCallID, turns, contextTokens, contextWindow)
		}
	}

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
					reportProgress()
				}

			case "tool_result":

			case "approval_request":
				if ctx.Err() != nil {
					goto done
				}
				approved := false
				if t.approvalFn != nil {
					approved = t.approvalFn(ev.Command, ev.Description, ev.Cwd, ev.Agent, ev.Risk)
				}
				if err := child.SendApprovalSafe(approved); err != nil {
					childErr = "approval response failed: " + err.Error()
					goto done
				}

			case "done":
				success = ev.Success
				if ev.Turns > 0 {
					turns = ev.Turns
					contextTokens, contextWindow = ev.ContextTokens, ev.ContextWindow
					reportProgress() // final count, before the card flips to done
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
	result := output.String()
	result += fmt.Sprintf("\n\n---\nSubagent finished. %d tool calls, %d turns.", toolCount, turns)
	if childErr != "" {
		result += "\nError: " + childErr
		return ToolResult{Content: result, Error: childErr}, nil
	}
	if !success {
		result += " (subagent reported failure)"
	}
	return ToolResult{Content: result}, nil
}
