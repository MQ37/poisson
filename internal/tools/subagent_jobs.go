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
	if j.status == "done" || j.status == "error" {
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
	if j.status == "done" || j.status == "error" {
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
