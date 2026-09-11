package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// elapsed reports a job's running (or final, if finished) duration, rounded
// to the second for a stable, readable display.
func (j subagentJobView) elapsed() time.Duration {
	end := j.doneAt
	if end.IsZero() {
		end = time.Now()
	}
	return end.Sub(j.startedAt).Round(time.Second)
}

// formatJobLine renders one job as a single summary line for subagent_status
// with no jobId (list-all).
func formatJobLine(j subagentJobView) string {
	line := fmt.Sprintf("%s  %s  %s  %s elapsed", j.id, j.name, j.status, j.elapsed())
	if j.status == "running" && j.turns > 0 {
		line += fmt.Sprintf("  %d turns", j.turns)
		if j.contextWindow > 0 {
			line += fmt.Sprintf("  %d/%d ctx tokens", j.contextTokens, j.contextWindow)
		}
	}
	if isTerminalJobStatus(j.status) {
		if j.retrieved {
			line += "  (retrieved)"
		} else {
			line += "  (ready — call subagent_result)"
		}
	}
	return line
}

// formatJobStatus renders one job's full status for subagent_status with an
// explicit jobId.
func formatJobStatus(j subagentJobView) string {
	var b strings.Builder
	fmt.Fprintf(&b, "job %s (%s)\n", j.id, j.name)
	fmt.Fprintf(&b, "task: %s\n", j.task)
	fmt.Fprintf(&b, "ran on: %s/%s", j.provider, j.model)
	if j.effort != "" {
		fmt.Fprintf(&b, " (%s effort)", j.effort)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "status: %s, elapsed: %s\n", j.status, j.elapsed())
	if (j.status == "running" || j.status == "queued") && j.turns > 0 {
		fmt.Fprintf(&b, "%d turns", j.turns)
		if j.contextWindow > 0 {
			fmt.Fprintf(&b, ", %d/%d context tokens", j.contextTokens, j.contextWindow)
		}
		if j.tokensPerSec > 0 {
			fmt.Fprintf(&b, ", %.0f tok/s", j.tokensPerSec)
		}
		b.WriteString("\n")
	}
	if isTerminalJobStatus(j.status) {
		if j.retrieved {
			b.WriteString("result already retrieved via subagent_result\n")
		} else {
			b.WriteString("ready — call subagent_result to retrieve the final output\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// SubagentStatusTool reports the status of jobs spawned via the subagent
// tool — one job by ID, or every job this session has spawned.
type SubagentStatusTool struct {
	owner *SubagentTool
}

// NewSubagentStatusTool creates a status tool reading owner's job registry.
func NewSubagentStatusTool(owner *SubagentTool) *SubagentStatusTool {
	return &SubagentStatusTool{owner: owner}
}

func (t *SubagentStatusTool) Name() string { return "subagent_status" }

func (t *SubagentStatusTool) Description() string {
	return "Check the status of subagent jobs spawned via the subagent tool. Omit jobId to list every job this session has spawned; pass jobId to check one. Running jobs show turn count and context usage; finished jobs show whether the result is ready to retrieve via subagent_result (each job's result can only be retrieved once)."
}

func (t *SubagentStatusTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"jobId": {"type": "string", "description": "Job ID returned by the subagent tool's spawn ack. Omit to list every job."}
		}
	}`)
}

func (t *SubagentStatusTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	var params struct {
		JobID string `json:"jobId"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &params); err != nil {
			return ToolResult{Error: "invalid input: " + err.Error()}, nil
		}
	}
	if params.JobID != "" {
		job, ok := t.owner.getJob(params.JobID)
		if !ok {
			return ToolResult{Error: fmt.Sprintf("no such subagent job: %s", params.JobID)}, nil
		}
		return ToolResult{Content: formatJobStatus(job.view())}, nil
	}
	jobs := t.owner.listJobs()
	if len(jobs) == 0 {
		return ToolResult{Content: "no subagent jobs spawned yet"}, nil
	}
	lines := make([]string, len(jobs))
	for i, j := range jobs {
		lines[i] = formatJobLine(j)
	}
	return ToolResult{Content: strings.Join(lines, "\n")}, nil
}

// SubagentResultTool retrieves a finished subagent job's final output —
// exactly once per job (see SubagentTool.retrieveJob).
type SubagentResultTool struct {
	owner *SubagentTool
}

// NewSubagentResultTool creates a result tool reading owner's job registry.
func NewSubagentResultTool(owner *SubagentTool) *SubagentResultTool {
	return &SubagentResultTool{owner: owner}
}

func (t *SubagentResultTool) Name() string { return "subagent_result" }

func (t *SubagentResultTool) Description() string {
	return "Retrieve a finished subagent job's final output (spawned via the subagent tool). Errors if the job is still queued/running (check subagent_status first) or if its result was already retrieved once — each job's result can only be retrieved a single time."
}

func (t *SubagentResultTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"jobId": {"type": "string", "description": "Job ID returned by the subagent tool's spawn ack."}
		},
		"required": ["jobId"]
	}`)
}

func (t *SubagentResultTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	var params struct {
		JobID string `json:"jobId"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return ToolResult{Error: "invalid input: " + err.Error()}, nil
	}
	if params.JobID == "" {
		return ToolResult{Error: "jobId is required"}, nil
	}
	job, ok := t.owner.getJob(params.JobID)
	if !ok {
		return ToolResult{Error: fmt.Sprintf("no such subagent job: %s", params.JobID)}, nil
	}
	return t.owner.retrieveJob(job), nil
}

// SubagentKillTool stops a running/queued subagent job on request — the
// model-facing counterpart to SubagentTool.KillAll (process shutdown only,
// unscoped). Session-scoped like subagent_status/subagent_result: a job
// spawned under a session the caller has since switched away from reads as
// not found, same as it does for those two tools.
type SubagentKillTool struct {
	owner *SubagentTool
}

// NewSubagentKillTool creates a kill tool acting on owner's job registry.
func NewSubagentKillTool(owner *SubagentTool) *SubagentKillTool {
	return &SubagentKillTool{owner: owner}
}

func (t *SubagentKillTool) Name() string { return "subagent_kill" }

func (t *SubagentKillTool) Description() string {
	return "Kill one running/queued subagent job by ID, or every job this session has spawned (all: true). Irreversible — the job stops immediately and its partial output (if any) is still retrievable once via subagent_result, but it does no further work. Exactly one of jobId or all is required."
}

func (t *SubagentKillTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"jobId": {"type": "string", "description": "Job ID returned by the subagent tool's spawn ack. Omit when using all."},
			"all": {"type": "boolean", "description": "Kill every non-finished job this session has spawned instead of a single one."}
		}
	}`)
}

func (t *SubagentKillTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	var params struct {
		JobID string `json:"jobId"`
		All   bool   `json:"all"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &params); err != nil {
			return ToolResult{Error: "invalid input: " + err.Error()}, nil
		}
	}
	switch {
	case params.All && params.JobID != "":
		return ToolResult{Error: "specify either jobId or all, not both"}, nil
	case params.All:
		n := t.owner.KillVisibleJobs()
		return ToolResult{Content: fmt.Sprintf("killed %d job(s)", n)}, nil
	case params.JobID != "":
		if err := t.owner.KillJob(params.JobID); err != nil {
			return ToolResult{Error: err.Error()}, nil
		}
		return ToolResult{Content: fmt.Sprintf("job %s killed", params.JobID)}, nil
	default:
		return ToolResult{Error: "jobId or all is required"}, nil
	}
}
