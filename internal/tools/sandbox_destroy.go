package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mq37/poisson/internal/sandbox"
)

// SandboxDestroyTool tears down a sandbox: kills its container via Manager
// and removes its scratch workspace directory tree. Always auto-approved —
// destroying a sandbox only discards disposable container/scratch-dir
// state, never host data, so there's nothing to gate (see
// docs/sandbox-plan.md's "Tool schema changes" section).
type SandboxDestroyTool struct {
	mgr *sandbox.Manager
}

func NewSandboxDestroyTool(mgr *sandbox.Manager) *SandboxDestroyTool {
	return &SandboxDestroyTool{mgr: mgr}
}

func (t *SandboxDestroyTool) Name() string { return "sandbox_destroy" }

func (t *SandboxDestroyTool) Description() string {
	return "Destroy a sandbox created by create_sandbox: kills its container and removes its scratch workspace. Always allowed with no approval prompt — this only discards disposable sandbox state, never host data. Destroying an unknown or already-destroyed sandboxId is a normal error, not a crash."
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

	sb, ok := t.mgr.Get(in.SandboxID)
	if !ok {
		return ToolResult{Error: fmt.Sprintf("sandbox %q not found — it may belong to a different session or already be destroyed", in.SandboxID)}, nil
	}

	if err := t.mgr.Kill(ctx, in.SandboxID); err != nil {
		return ToolResult{Error: "destroy sandbox: " + err.Error()}, nil
	}

	// sb.HostPath is <base>/workspace (see newScratchWorkspace); remove the
	// whole <base> tree, not just the workspace subdirectory.
	if sb.HostPath != "" {
		os.RemoveAll(filepath.Dir(sb.HostPath))
	}

	return ToolResult{Content: fmt.Sprintf("sandbox %s destroyed", in.SandboxID)}, nil
}
