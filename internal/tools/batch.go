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
// batch — no recursion.
//
// bash and subagent are allowed (see mutatingTools below for why subagent
// runs serial) but stay fully gated: bash dispatched through batch reaches
// the same BashTool.Execute as a direct call, so the LLM risk classifier and
// human-approval prompt still apply per call — batch has no side channel
// that bypasses ApprovalFn. A subagent's own registry never
// registers the subagent tool in the first place (BuildRegistry,
// opts.Child), so a spawned child dispatching subagent through batch still
// fails with "tool not registered": recursion stays bounded to one level
// regardless of the batch path.
var batchDenied = map[string]bool{
	"batch": true,
}

const (
	batchMaxCalls     = 20
	batchStepMaxBytes = 8 * 1024 // per-step body in the aggregate result
	// batchMaxConcurrent bounds how many of a batch's own calls run at
	// once — mirrors agent.maxConcurrentToolCalls (same value), which caps
	// the model's native parallel tool_use path for the identical reason:
	// a batch call, including one whose steps are themselves subagent
	// spawns, previously had no ceiling at all here, so up to batchMaxCalls
	// (20) child px processes could launch simultaneously from a single
	// tool_use.
	batchMaxConcurrent = 8
)

// BatchTool runs multiple independent tool calls in one invocation — a
// polyfill for models/providers that only emit a single tool_use per turn.
// No dataflow between steps: every call's args must be fully specified up
// front. Prefer native parallel tool_use when the model supports it.
type BatchTool struct {
	reg *Registry
	// subagentDoneFn, if set, is called immediately after each nested
	// subagent call finishes — see SetSubagentDoneFn.
	subagentDoneFn func(toolCallID string, res ToolResult)
}

// NewBatchTool wires batch against the registry it will dispatch into.
// Register the returned tool on that same registry after the other tools.
func NewBatchTool(reg *Registry) *BatchTool {
	return &BatchTool{reg: reg}
}

// SetSubagentDoneFn wires the callback invoked right after a nested subagent
// call inside this batch finishes. A batched subagent gets its own live TUI
// widget (agent.go pre-renders one per nested subagent call, keyed by
// BatchCallID, before Execute even starts) — this is how that widget learns
// to flip from spinner to done/error the moment ITS call finishes, rather
// than only when the whole batch does.
func (t *BatchTool) SetSubagentDoneFn(fn func(toolCallID string, res ToolResult)) {
	t.subagentDoneFn = fn
}

// BatchCallSpec is one entry of a batch tool's calls array — exported so a
// caller (agent.go) can pre-inspect nested calls without re-deriving batch's
// JSON shape itself.
type BatchCallSpec struct {
	Tool  string
	Input json.RawMessage
}

// ParseBatchCalls parses a batch tool's raw input into its call list.
// Returns nil on any parse error — callers treat that the same as an
// ordinary tool call with unparseable input: nothing to pre-render.
func ParseBatchCalls(input json.RawMessage) []BatchCallSpec {
	var in batchInput
	if json.Unmarshal(input, &in) != nil {
		return nil
	}
	specs := make([]BatchCallSpec, len(in.Calls))
	for i, c := range in.Calls {
		// Same canonicalization the dispatch path applies, so a pre-rendered
		// card (and agent.go's nested-call inspection) names the tool the way
		// it will actually run.
		specs[i] = BatchCallSpec{Tool: stripWireToolPrefix(strings.TrimSpace(c.Tool)), Input: c.Input}
	}
	return specs
}

// BatchCallID derives the synthetic per-nested-call tool-call-id used to
// give a batched subagent call its own live TUI widget. The same
// deterministic derivation runs on both ends — here (threaded into the
// nested call's context) and in agent.go (pre-rendering the widget before
// batch even starts) — so they agree with no explicit handshake.
func BatchCallID(outerID string, i int) string {
	return fmt.Sprintf("%s.%d", outerID, i)
}

func (t *BatchTool) Name() string { return "batch" }

func (t *BatchTool) Description() string {
	return "Run multiple independent tool calls in one step (max 20). Polyfill for models that only emit one tool_use per turn — prefer native parallel tool calls when available. No dataflow between steps: each call needs fully-specified args. Allowed: every registered tool except batch (no recursion) — bash is allowed and still fully gated (LLM risk classifier + human approval, same as calling it directly, no bypass); subagent is allowed and runs fully in parallel with the batch's other subagent calls (this is the way to spawn several concurrent subagents in one turn for a model that can only emit one tool_use per turn). Read-only steps and subagent all run in parallel; any edit/write/bash/create_sandbox/sandbox_cp makes the whole batch serial, in the order listed, so approval prompts stay in submission order. Validation errors (unknown/denied tool, empty calls, over cap) reject the whole batch before any step runs; runtime errors (bad path, edit miss, denied approval, …) are per-step — other steps still run, no rollback."
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
//
// bash, create_sandbox and sandbox_cp are approval-gated the same way edit/
// write are (see agent.go's approvalGatedTools) — each independently
// reaches TUI.Approve(), which is single-flight but has no ordering guarantee
// across concurrent goroutines. Without these three here, a batch made of
// e.g. two bash calls (no edit/write present) would dispatch them
// through the concurrent path below, each hitting the approval prompt in
// whatever order the Go scheduler happens to wake them — the exact race
// agent.go's sequential gated walker exists to prevent at the top level,
// reopened one level down inside batch's own dispatch. Forcing serial here
// closes that: every approval-gated tool this package knows about is
// mutating for batch's purposes, gated or actually-mutating alike.
//
// subagent deliberately is NOT here (same reasoning as agent.go's
// approvalGatedTools, which also dropped it): its own approval prompt, if
// any, is relayed from the child at an arbitrary point during a run that
// may take minutes, not near the start of Execute — there's no real
// "submission order" relationship to protect, unlike bash/edit/write/
// create_sandbox/sandbox_cp which ask immediately. This one was the second
// half of the "only the first scout's turns move" bug: a model that can
// only emit one tool_use per turn has no way to spawn N parallel subagents
// except by wrapping them all in a single `batch` call, so having subagent
// here forced every batched scout to run one at a time regardless of the
// top-level fix in agent.go's approvalGatedTools (which only ever sees the
// single top-level "batch" tool_use, never what's nested inside it). See
// TestBatch_ParallelSubagentsRunConcurrently.
var mutatingTools = map[string]bool{
	"edit":           true,
	"write":          true,
	"bash":           true,
	"create_sandbox": true,
	"sandbox_cp":     true,
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

	if t.reg == nil {
		return ToolResult{Error: "batch has no registry"}, nil
	}
	// Validate names up front so a typo doesn't half-apply a batch. Names are
	// canonicalized first (mcp_Batch -> batch): the deny list, the
	// mutating-tool set and the subagent check below all key off the
	// registered spelling, so a wire-prefixed name must not reach them raw.
	for i := range in.Calls {
		in.Calls[i].Tool = t.reg.Canonical(strings.TrimSpace(in.Calls[i].Tool))
	}
	for i, c := range in.Calls {
		name := c.Tool
		if name == "" {
			return ToolResult{Error: fmt.Sprintf("call %d: tool is required", i+1)}, nil
		}
		if batchDenied[name] {
			return ToolResult{Error: fmt.Sprintf("call %d: tool %q is not allowed inside batch (denied: batch, no recursion)", i+1, name)}, nil
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
		if mutatingTools[c.Tool] {
			serial = true
			break
		}
	}

	outs := make([]batchStepOut, len(in.Calls))
	outerID, hasOuterID := ToolCallIDFromContext(ctx)

	runOne := func(i int) {
		c := in.Calls[i]
		name := c.Tool
		callCtx := ctx
		if hasOuterID {
			callCtx = WithToolCallID(ctx, BatchCallID(outerID, i))
		}
		res, err := t.reg.Execute(callCtx, name, c.Input)
		// Registry.Execute returns (trimmed result, err) for unknown tools;
		// known tools put failures in res.Error and err==nil.
		if err != nil && res.Error == "" {
			res.Error = err.Error()
		}
		if name == "subagent" && hasOuterID && t.subagentDoneFn != nil {
			t.subagentDoneFn(BatchCallID(outerID, i), res)
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
						name := strings.TrimSpace(in.Calls[j].Tool)
						outs[j] = batchStepOut{
							content: fmt.Sprintf("%d. %s — error: cancelled", j+1, name),
							isErr:   true,
						}
						// A batched subagent call gets its own live TUI
						// widget the moment agent.go pre-renders it, before
						// this call ever runs (see SetSubagentDoneFn's doc
						// comment) — one that was never reached because the
						// batch was cancelled first must still be told so,
						// or its widget spins forever with no tool_result
						// ever emitted for it. runOne fires this same
						// callback on every real completion; a cancelled,
						// never-started call needs the identical signal.
						if name == "subagent" && hasOuterID && t.subagentDoneFn != nil {
							t.subagentDoneFn(BatchCallID(outerID, j), ToolResult{Error: "cancelled"})
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
		sem := make(chan struct{}, batchMaxConcurrent)
		for i := range in.Calls {
			i := i
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
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
