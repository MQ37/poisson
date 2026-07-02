package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"poisson/internal/store"
	"poisson/internal/subagent"
)

// SubagentOutput streams subagent events to the parent UI.
// eventType is text | tool_start | tool_result; toolErr is set for tool_result errors.
type SubagentOutput func(eventType, text, toolName string, toolInput json.RawMessage, toolErr string)

// SubagentApproval handles bash approval requests forwarded from the child.
// risk is the child's assessed level (low/medium/high); empty means unknown.
type SubagentApproval func(command, description, workdir, agentName, risk string) bool

// SubagentTool spawns a one-shot child Poisson process for isolated work.
type SubagentTool struct {
	cwd        string
	store      *store.Store
	providerFn func() string
	modelFn    func() string
	effortFn   func() string
	outputFn   SubagentOutput
	approvalFn SubagentApproval
}

// NewSubagentTool creates a subagent tool.
func NewSubagentTool(cwd string, st *store.Store, outputFn SubagentOutput, approvalFn SubagentApproval) *SubagentTool {
	return &SubagentTool{
		cwd:        cwd,
		store:      st,
		outputFn:   outputFn,
		approvalFn: approvalFn,
	}
}

// SetRuntime supplies live provider/model/effort resolvers (called at spawn time).
func (t *SubagentTool) SetRuntime(providerFn, modelFn, effortFn func() string) {
	t.providerFn = providerFn
	t.modelFn = modelFn
	t.effortFn = effortFn
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
	if err := t.store.CreateSession(&store.Session{
		ID:         childSessionID,
		Cwd:        t.cwd,
		Provider:   prov,
		Model:      model,
		IsSubagent: true,
	}); err != nil {
		return ToolResult{Error: "failed to create subagent session: " + err.Error()}, nil
	}

	child, err := subagent.Spawn(subagent.SpawnInput{
		Task:      params.Task,
		Cwd:       t.cwd,
		SessionID: childSessionID,
		Name:      agentName,
		Provider:  prov,
		Model:     model,
		Effort:    effort,
	})
	if err != nil {
		return ToolResult{Error: "failed to spawn subagent: " + err.Error()}, nil
	}
	defer child.Reap()

	var output strings.Builder
	var toolCount, turns int
	var success bool
	var childErr string

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

			switch ev.Type {
			case "text":
				output.WriteString(ev.Text)
				if t.outputFn != nil {
					t.outputFn("text", ev.Text, "", nil, "")
				}

			case "tool":
				if t.outputFn != nil {
					t.outputFn("tool_start", "", ev.Tool, ev.ToolInput, "")
				}
				toolCount++

			case "tool_result":
				if t.outputFn != nil {
					t.outputFn("tool_result", ev.Result, ev.Tool, nil, ev.Error)
				}

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