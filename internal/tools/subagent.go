package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"poisson/internal/store"
	"poisson/internal/subagent"
)

// SubagentOutput is a callback for streaming subagent events to the caller.
// The caller (agent) wraps this to send events to its output channel.
type SubagentOutput func(eventType, text, toolName string, toolInput json.RawMessage)

// SubagentApproval is a callback for bash approval requests from the child.
type SubagentApproval func(command, description, workdir, agentName string) bool

// SubagentTool spawns a one-shot child Poisson process for isolated work.
type SubagentTool struct {
	cwd        string
	store      *store.Store
	provider   string
	model      string
	outputFn   SubagentOutput
	approvalFn SubagentApproval
}

// NewSubagentTool creates a subagent tool.
func NewSubagentTool(cwd, provider, model string, st *store.Store, outputFn SubagentOutput, approvalFn SubagentApproval) *SubagentTool {
	return &SubagentTool{
		cwd:        cwd,
		store:      st,
		provider:   provider,
		model:      model,
		outputFn:   outputFn,
		approvalFn: approvalFn,
	}
}

func (t *SubagentTool) Name() string { return "subagent" }

func (t *SubagentTool) Description() string {
	return "Spawn a one-shot child Poisson agent to complete a specific task. The child has read, write, edit, bash, search, ls, and glob tools. Use when you need focused work isolated from the main session. The child returns its final output when done. It cannot ask questions — give it a complete, self-contained task."
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

	// Create a child session in the store.
	childSessionID := fmt.Sprintf("sub-%d", time.Now().UnixNano())
	prov := t.provider
	model := t.model
	if prov == "" {
		prov = "ollama"
	}
	if model == "" {
		model = "glm-5.2:cloud"
	}
	t.store.CreateSession(&store.Session{
		ID:         childSessionID,
		Cwd:        t.cwd,
		Provider:   prov,
		Model:      model,
		IsSubagent: true,
		CreatedAt:  time.Now().Unix(),
		UpdatedAt:  time.Now().Unix(),
	})

	child, err := subagent.Spawn(subagent.SpawnInput{
		Task:      params.Task,
		Cwd:       t.cwd,
		SessionID: childSessionID,
		Name:      agentName,
		Provider:  prov,
		Model:     model,
	})
	if err != nil {
		return ToolResult{Error: "failed to spawn subagent: " + err.Error()}, nil
	}
	defer child.Kill()

	// Read events from child.
	var output strings.Builder
	var toolCount, turns int
	var success bool
	var childErr string

	for {
		select {
		case <-ctx.Done():
			return ToolResult{Content: output.String(), Error: "subagent cancelled"}, nil
		default:
		}

		ev, err := child.ReadEvent()
		if err != nil {
			break
		}
		if ev == nil {
			continue
		}

		switch ev.Type {
		case "text":
			output.WriteString(ev.Text)
			if t.outputFn != nil {
				t.outputFn("text", ev.Text, "", nil)
			}

		case "tool":
			if t.outputFn != nil {
				t.outputFn("tool_start", "", ev.Tool, ev.ToolInput)
			}
			toolCount++

		case "approval_request":
			approved := false
			if t.approvalFn != nil {
				approved = t.approvalFn(ev.Command, ev.Description, ev.Cwd, ev.Agent)
			}
			child.SendApprovalSafe(approved)

		case "done":
			success = ev.Success
			turns = ev.Turns
			if ev.Text != "" {
				output.WriteString(ev.Text)
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

done:
	child.Wait()

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
