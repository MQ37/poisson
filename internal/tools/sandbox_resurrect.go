package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mq37/poisson/internal/sandbox"
)

// SandboxResurrectTool resumes a stopped-but-not-removed sandbox container
// back to running. A container survives a poisson crash/restart as a
// stopped podman container (list_sandboxes already shows it, with
// running=false) — this is the tool that brings it back instead of forcing
// a fresh create_sandbox (which would lose whatever state the container
// accumulated: installed packages, files outside the bind-mounted
// /workspace, shell history). Never touches any host directory — the
// container's own filesystem persists across stop/start on its own.
// Always allowed with no approval prompt — this only resumes a container
// this session already owns or can discover, same reasoning as
// sandbox_destroy: nothing new is granted that create_sandbox didn't
// already gate.
type SandboxResurrectTool struct {
	mgr *sandbox.Manager
}

func NewSandboxResurrectTool(mgr *sandbox.Manager) *SandboxResurrectTool {
	return &SandboxResurrectTool{mgr: mgr}
}

func (t *SandboxResurrectTool) Name() string { return "sandbox_resurrect" }

func (t *SandboxResurrectTool) Description() string {
	return "Resume a stopped (but not destroyed) sandbox container back to running — use when list_sandboxes shows one with running=false, e.g. after a restart or crash. Safe to call on an already-running sandbox (no-op). Returns {sandboxId, hostPath}, same shape as create_sandbox — pass sandboxId to bash's own sandboxId param afterward. Never recreates or loses the container's state (installed packages, files outside /workspace, shell history all persist); use create_sandbox instead if you actually want a fresh one. Always allowed with no approval prompt."
}

func (t *SandboxResurrectTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "sandboxId": { "type": "string", "description": "The stopped sandbox's id, as shown by list_sandboxes" }
  },
  "required": ["sandboxId"]
}`)
}

type sandboxResurrectInput struct {
	SandboxID string `json:"sandboxId"`
}

func (t *SandboxResurrectTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	if t.mgr == nil {
		return ToolResult{Error: "sandboxing is not available in this session"}, nil
	}
	var in sandboxResurrectInput
	if err := json.Unmarshal(input, &in); err != nil {
		return ToolResult{Error: "invalid input: " + err.Error()}, nil
	}
	if in.SandboxID == "" {
		return ToolResult{Error: "sandboxId is required"}, nil
	}

	sb, err := t.mgr.Resurrect(ctx, in.SandboxID)
	if err != nil {
		return ToolResult{Error: fmt.Sprintf("resurrect sandbox %q: %v", in.SandboxID, err)}, nil
	}

	data, _ := json.Marshal(map[string]string{"sandboxId": sb.ID, "hostPath": sb.HostPath})
	return ToolResult{Content: string(data)}, nil
}
