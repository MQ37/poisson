package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mq37/poisson/internal/sandbox"
)

// SandboxDestroyTool tears down a sandbox: kills its container via Manager.
// Never touches any host directory — hostPath (if the sandbox had one) is
// always agent-supplied, never ours to delete. Always auto-approved —
// destroying a sandbox only discards the container, never host data, so
// there's nothing to gate (see docs/sandbox-plan.md's "Tool schema changes"
// section).
type SandboxDestroyTool struct {
	mgr *sandbox.Manager
}

func NewSandboxDestroyTool(mgr *sandbox.Manager) *SandboxDestroyTool {
	return &SandboxDestroyTool{mgr: mgr}
}

func (t *SandboxDestroyTool) Name() string { return "sandbox_destroy" }

func (t *SandboxDestroyTool) Description() string {
	return "Destroy a sandbox created by create_sandbox: kills its container. Any host directory you mounted into it (hostPath/mounts) is left untouched — sandbox_destroy never deletes host paths. Always allowed with no approval prompt — this only discards the disposable container itself. Destroying an unknown or already-destroyed sandboxId is a normal error, not a crash."
}

func (t *SandboxDestroyTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "sandboxId": { "type": "string" }
  },
  "required": ["sandboxId"]
}`)
}

type sandboxDestroyInput struct {
	SandboxID string `json:"sandboxId"`
}

func (t *SandboxDestroyTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	if t.mgr == nil {
		return ToolResult{Error: "sandboxing is not available in this session"}, nil
	}
	var in sandboxDestroyInput
	if err := json.Unmarshal(input, &in); err != nil {
		return ToolResult{Error: "invalid input: " + err.Error()}, nil
	}
	if in.SandboxID == "" {
		return ToolResult{Error: "sandboxId is required"}, nil
	}

	// No separate Get pre-check: Get/Owns go through attach()'s running-only
	// discovery filter, which would wrongly reject a stopped-but-real
	// foreign sandbox here. Kill resolves ownership itself (owned, or
	// discoverable by name regardless of running state) and returns its own
	// clear not-found error otherwise.
	if err := t.mgr.Kill(ctx, in.SandboxID); err != nil {
		return ToolResult{Error: "destroy sandbox: " + err.Error()}, nil
	}

	return ToolResult{Content: fmt.Sprintf("sandbox %s destroyed", in.SandboxID)}, nil
}
