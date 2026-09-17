package tools

import (
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

// formatJobLine renders one job as a single summary line for action=status
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
			line += "  (ready — call action=result)"
		}
	}
	return line
}

// formatJobStatus renders one job's full status for action=status with an
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
			b.WriteString("result already retrieved via action=result\n")
		} else {
			b.WriteString("ready — call action=result to retrieve the final output\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
