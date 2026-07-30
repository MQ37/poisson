package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mq37/poisson/internal/sandbox"
)

// defaultSandboxImage is the pinned base image for a new sandbox — never a
// floating tag like "latest" (see docs/sandbox-plan.md's "Image" section).
const defaultSandboxImage = "ubuntu:26.04"

// CreateSandboxTool creates a new sandbox container and hands back a plain
// host path (see docs/sandbox-plan.md's "File tools" section — read/write/
// edit/grep/glob never take a sandboxId, they just operate on this path).
//
// No bootstrap (matching-uid user + passwordless sudo) happens here yet —
// that's real podman-specific behavior deferred to the podmanDriver step
// (docs/sandbox-plan.md), not guessed at against a driver that doesn't
// exist yet.
type CreateSandboxTool struct {
	cwd string // session root; scratch workspace dirs are still made under os.TempDir(), this is only used in approval prompts
	mgr *sandbox.Manager
	// approvalFn is asked only when the request includes mounts or env
	// beyond the base workspace — the base workspace itself never needs
	// approval (see docs/sandbox-plan.md's "Approval" section).
	approvalFn ApprovalFn
}

func NewCreateSandboxTool(cwd string, mgr *sandbox.Manager, approvalFn ApprovalFn) *CreateSandboxTool {
	return &CreateSandboxTool{cwd: cwd, mgr: mgr, approvalFn: approvalFn}
}

func (t *CreateSandboxTool) Name() string { return "create_sandbox" }

func (t *CreateSandboxTool) Description() string {
	return "Create a podman sandbox container for running untrusted/experimental bash commands with no approval gate — the container's own isolation is the safety boundary. Returns {sandboxId, hostPath}: pass sandboxId to bash's own sandboxId param to run commands in it, and use the existing read/write/edit/grep/glob tools with absolute paths under hostPath to inspect/edit its files (no sandboxId param on those — hostPath is just a plain host directory). Requesting mounts or env beyond the base workspace needs human approval; a plain create_sandbox call with neither does not."
}

func (t *CreateSandboxTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "image": { "type": "string", "description": "Container image (default: a pinned Ubuntu LTS)" },
    "mounts": {
      "type": "array",
      "description": "Extra host bind mounts beyond the base workspace (e.g. a second project, credentials) — requires human approval, showing the exact host paths.",
      "items": {
        "type": "object",
        "properties": {
          "hostPath": { "type": "string" },
          "containerPath": { "type": "string" },
          "readOnly": { "type": "boolean" }
        },
        "required": ["hostPath", "containerPath"]
      }
    },
    "env": {
      "type": "array",
      "items": { "type": "string" },
      "description": "Extra KEY=VALUE entries injected into the container — requires human approval."
    }
  }
}`)
}

type createSandboxInput struct {
	Image  string          `json:"image"`
	Mounts []sandbox.Mount `json:"mounts"`
	Env    []string        `json:"env"`
}

func (t *CreateSandboxTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	if t.mgr == nil {
		return ToolResult{Error: "sandboxing is not available in this session"}, nil
	}
	var in createSandboxInput
	if len(input) > 0 {
		if err := json.Unmarshal(input, &in); err != nil {
			return ToolResult{Error: "invalid input: " + err.Error()}, nil
		}
	}

	if len(in.Mounts) > 0 || len(in.Env) > 0 {
		desc := describeSandboxRequest(in.Mounts, in.Env)
		approved, reason := false, ""
		if t.approvalFn != nil {
			approved, reason = t.approvalFn(ctx, desc, "create_sandbox requests host access beyond its own scratch workspace", t.cwd)
		}
		if !approved {
			msg := "sandbox creation rejected by user"
			if reason = strings.TrimSpace(reason); reason != "" {
				msg += " - reason: " + reason
			}
			return ToolResult{Error: msg}, nil
		}
	}

	image := strings.TrimSpace(in.Image)
	if image == "" {
		image = defaultSandboxImage
	}

	hostPath, cleanup, err := newScratchWorkspace()
	if err != nil {
		return ToolResult{Error: "cannot create sandbox workspace: " + err.Error()}, nil
	}

	sb, err := t.mgr.Create(ctx, sandbox.CreateOpts{
		Image:    image,
		HostPath: hostPath,
		Mounts:   in.Mounts,
		Env:      in.Env,
	})
	if err != nil {
		cleanup()
		return ToolResult{Error: "create sandbox: " + err.Error()}, nil
	}

	data, _ := json.Marshal(map[string]string{"sandboxId": sb.ID, "hostPath": sb.HostPath})
	return ToolResult{Content: string(data)}, nil
}

// describeSandboxRequest renders the exact host paths/mode a create_sandbox
// call is asking for, so the human approval prompt shows precisely what's
// being requested — never just "mounts requested: yes".
func describeSandboxRequest(mounts []sandbox.Mount, env []string) string {
	var b strings.Builder
	b.WriteString("create_sandbox")
	for _, m := range mounts {
		mode := "rw"
		if m.ReadOnly {
			mode = "ro"
		}
		fmt.Fprintf(&b, " --mount %s:%s:%s", m.HostPath, m.ContainerPath, mode)
	}
	for _, e := range env {
		// Show only the key, never the value: env entries are frequently
		// secrets/tokens, and the approval prompt is not the place to echo
		// one back in full.
		key, _, _ := strings.Cut(e, "=")
		fmt.Fprintf(&b, " --env %s=<redacted>", key)
	}
	return b.String()
}

// newScratchWorkspace creates a fresh, private (0700, os.MkdirTemp's
// default) host directory tree for one sandbox's base workspace mount:
// <base>/workspace is what's bind-mounted and handed to the agent as
// hostPath; <base> itself exists so a later teardown can remove the whole
// tree in one os.RemoveAll rather than needing to know sibling-directory
// layout. cleanup removes the whole tree — call it if sandbox creation
// fails after the directory was already made.
func newScratchWorkspace() (hostPath string, cleanup func(), err error) {
	base, err := os.MkdirTemp("", "poisson-sandbox-*")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { os.RemoveAll(base) }
	ws := filepath.Join(base, "workspace")
	if err := os.Mkdir(ws, 0o755); err != nil {
		cleanup()
		return "", nil, err
	}
	return ws, cleanup, nil
}
