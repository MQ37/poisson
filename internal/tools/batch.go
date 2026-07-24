package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// Tools that must never run inside batch.
//
// bash  — shell is the escape hatch; packing it into batch defeats the point
// and reintroduces untyped pipelines under a "safe" name.
//
// subagent — long-lived nested agent loop with its own TUI/approvals; cannot
// sensibly sit as one step among reads/edits.
//
// batch — no recursion.
var batchDenied = map[string]bool{
	"bash":     true,
	"subagent": true,
	"batch":    true,
}

const (
	batchMaxCalls     = 20
	batchStepMaxBytes = 8 * 1024 // per-step body in the aggregate result
)

// BatchTool runs multiple independent tool calls in one invocation — a
// polyfill for models/providers that only emit a single tool_use per turn.
// No dataflow between steps: every call's args must be fully specified up
// front. Prefer native parallel tool_use when the model supports it.
type BatchTool struct {
	reg *Registry
}

// NewBatchTool wires batch against the registry it will dispatch into.
// Register the returned tool on that same registry after the other tools.
func NewBatchTool(reg *Registry) *BatchTool {
	return &BatchTool{reg: reg}
}

func (t *BatchTool) Name() string { return "batch" }

func (t *BatchTool) Description() string {
	return "Run multiple independent tool calls in one step (max 20). Polyfill for models that only emit one tool_use per turn — prefer native parallel tool calls when available. No dataflow between steps: each call needs fully-specified args. Allowed: every registered tool except bash, subagent, and batch. Read-only steps may run in parallel; any edit/write makes the whole batch serial. Validation errors (unknown/denied tool, empty calls, over cap) reject the whole batch before any step runs; runtime errors (bad path, edit miss, …) are per-step — other steps still run, no rollback."
}

func (t *BatchTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "calls": {
      "type": "array",
      "description": "Independent tool calls, in order. Each item: {tool, input}.",
      "items": {
        "type": "object",
        "properties": {
          "tool": { "type": "string", "description": "Tool name (e.g. read, edit, grep, glob, write)" },
          "input": { "type": "object", "description": "Arguments for that tool (same schema as calling it directly)" }
        },
        "required": ["tool", "input"]
      }
    }
  },
  "required": ["calls"]
}`)
}

type batchCall struct {
	Tool  string          `json:"tool"`
	Input json.RawMessage `json:"input"`
}

type batchInput struct {
	Calls []batchCall `json:"calls"`
}

// mutatingTools force serial execution when present in the batch.
var mutatingTools = map[string]bool{
	"edit":  true,
	"write": true,
}

type batchStepOut struct {
	content string
	isErr   bool
}

func (t *BatchTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	var in batchInput
	if err := json.Unmarshal(input, &in); err != nil {
		return ToolResult{Error: "invalid input: " + err.Error()}, nil
	}
	if len(in.Calls) == 0 {
		return ToolResult{Error: "calls is empty — pass calls: [{tool, input}, ...]"}, nil
	}
	if len(in.Calls) > batchMaxCalls {
		return ToolResult{Error: fmt.Sprintf("too many calls (%d); max %d per batch", len(in.Calls), batchMaxCalls)}, nil
	}

	// Validate names up front so a typo doesn't half-apply a batch.
	for i, c := range in.Calls {
		name := strings.TrimSpace(c.Tool)
		if name == "" {
			return ToolResult{Error: fmt.Sprintf("call %d: tool is required", i+1)}, nil
		}
		if batchDenied[name] {
			return ToolResult{Error: fmt.Sprintf("call %d: tool %q is not allowed inside batch (denied: bash, subagent, batch)", i+1, name)}, nil
		}
		if t.reg == nil {
			return ToolResult{Error: "batch has no registry"}, nil
		}
		if _, ok := t.reg.Get(name); !ok {
			return ToolResult{Error: fmt.Sprintf("call %d: tool not registered: %s", i+1, name)}, nil
		}
		if len(c.Input) == 0 {
			return ToolResult{Error: fmt.Sprintf("call %d: input is required", i+1)}, nil
		}
	}

	serial := false
	for _, c := range in.Calls {
		if mutatingTools[strings.TrimSpace(c.Tool)] {
			serial = true
			break
		}
	}

	outs := make([]batchStepOut, len(in.Calls))

	runOne := func(i int) {
		c := in.Calls[i]
		name := strings.TrimSpace(c.Tool)
		res, err := t.reg.Execute(ctx, name, c.Input)
		// Registry.Execute returns (trimmed result, err) for unknown tools;
		// known tools put failures in res.Error and err==nil.
		if err != nil && res.Error == "" {
			res.Error = err.Error()
		}
		label := fmt.Sprintf("%d. %s", i+1, name)
		if res.Error != "" {
			outs[i] = batchStepOut{
				content: label + " — error: " + clipStep(res.Error),
				isErr:   true,
			}
			return
		}
		body := res.Content
		if body == "" {
			body = "(empty)"
		}
		outs[i] = batchStepOut{content: label + " — ok:\n" + clipStep(body)}
	}

	if serial || len(in.Calls) == 1 {
		for i := range in.Calls {
			if ctx != nil {
				select {
				case <-ctx.Done():
					for j := i; j < len(in.Calls); j++ {
						outs[j] = batchStepOut{
							content: fmt.Sprintf("%d. %s — error: cancelled", j+1, strings.TrimSpace(in.Calls[j].Tool)),
							isErr:   true,
						}
					}
					return summarizeBatch(outs), nil
				default:
				}
			}
			runOne(i)
		}
	} else {
		var wg sync.WaitGroup
		wg.Add(len(in.Calls))
		for i := range in.Calls {
			i := i
			go func() {
				defer wg.Done()
				runOne(i)
			}()
		}
		wg.Wait()
	}

	return summarizeBatch(outs), nil
}

func summarizeBatch(outs []batchStepOut) ToolResult {
	okN, errN := 0, 0
	var b strings.Builder
	for _, o := range outs {
		if o.isErr {
			errN++
		} else {
			okN++
		}
		b.WriteString(o.content)
		if !strings.HasSuffix(o.content, "\n") {
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}
	header := fmt.Sprintf("batch: %d calls, %d ok, %d err\n\n", len(outs), okN, errN)
	content := header + strings.TrimRight(b.String(), "\n") + "\n"
	// If everything failed, still return Content (not Error) so the model
	// sees per-step detail; a top-level Error would hide the breakdown.
	return ToolResult{Content: content}
}

func clipStep(s string) string {
	s = strings.TrimRight(s, "\n")
	if len(s) <= batchStepMaxBytes {
		return s
	}
	return utf8SafePrefix(s, batchStepMaxBytes) + fmt.Sprintf("\n... (%d more bytes in this step)", len(s)-batchStepMaxBytes)
}
