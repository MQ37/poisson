package tools

import (
	"context"
	"encoding/json"
	"fmt"
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
	cwd string // session root; only used in approval prompts
	mgr *sandbox.Manager
	// approvalFn is asked only when the request includes a hostPath, extra
	// mounts, or env — a plain create_sandbox call with none of those never
	// needs approval (see docs/sandbox-plan.md's "Approval" section).
	approvalFn ApprovalFn
}

func NewCreateSandboxTool(cwd string, mgr *sandbox.Manager, approvalFn ApprovalFn) *CreateSandboxTool {
	return &CreateSandboxTool{cwd: cwd, mgr: mgr, approvalFn: approvalFn}
}

func (t *CreateSandboxTool) Name() string { return "create_sandbox" }

func (t *CreateSandboxTool) Description() string {
	return "Create a podman sandbox container for running untrusted/experimental bash commands with no approval gate — the container's own isolation is the safety boundary. Returns {sandboxId, hostPath}: pass sandboxId to bash's own sandboxId param to run commands in it. There is no default workspace — pass hostPath yourself (any host directory you want bind-mounted as /workspace inside the container) if you want one; omit it for an isolated container with no host-backed directory at all. Whatever hostPath you give is used as-is, never copied or auto-created — use the existing read/write/edit/grep/glob tools with absolute paths under it to inspect/edit its files (no sandboxId param on those). Give the sandbox a descriptive name (e.g. \"api-testing-2\") so you — or another session, even after a crash — can find and reuse it later via list_sandboxes; sandboxId IS that name (prefixed px-sandbox-), not an opaque handle, and it's visible/usable across every session on this host, not just this one. A name already in use fails clearly; pick another or check list_sandboxes first — if it shows a matching sandbox with running=false, use sandbox_resurrect to resume it instead of creating a new one. Requesting hostPath, extra mounts, or env needs human approval, showing the exact paths; a plain create_sandbox call with none of those does not."
}

func (t *CreateSandboxTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "image": { "type": "string", "description": "Container image (default: a pinned Ubuntu LTS)" },
    "name": { "type": "string", "description": "Descriptive name for this sandbox (e.g. \"api-testing-2\") — becomes its sandboxId, prefixed px-sandbox-. Omit for a random name. Must be unique across every session on this host." },
    "hostPath": { "type": "string", "description": "Host directory to bind-mount as /workspace inside the container (used as-is — never auto-created, never a default /tmp dir). Omit for no workspace at all. Requires human approval, showing the exact path." },
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
	Image    string          `json:"image"`
	Name     string          `json:"name"`
	HostPath string          `json:"hostPath"`
	Mounts   []sandbox.Mount `json:"mounts"`
	Env      []string        `json:"env"`
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

	hostPath := strings.TrimSpace(in.HostPath)
	if hostPath != "" || len(in.Mounts) > 0 || len(in.Env) > 0 {
		desc := describeSandboxRequest(hostPath, in.Mounts, in.Env)
		approved, reason := false, ""
		if t.approvalFn != nil {
			approved, reason = t.approvalFn(ctx, desc, "create_sandbox requests host directory access", t.cwd)
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

	sb, err := t.mgr.Create(ctx, sandbox.CreateOpts{
		Image:    image,
		Name:     in.Name,
		HostPath: hostPath,
		Mounts:   in.Mounts,
		Env:      in.Env,
	})
	if err != nil {
		return ToolResult{Error: "create sandbox: " + err.Error()}, nil
	}

	data, _ := json.Marshal(map[string]string{"sandboxId": sb.ID, "hostPath": sb.HostPath})
	return ToolResult{Content: string(data)}, nil
}

// describeSandboxRequest renders the exact host paths/mode a create_sandbox
// call is asking for, so the human approval prompt shows precisely what's
// being requested — never just "mounts requested: yes".
func describeSandboxRequest(hostPath string, mounts []sandbox.Mount, env []string) string {
	var b strings.Builder
	b.WriteString("create_sandbox")
	if hostPath != "" {
		fmt.Fprintf(&b, " --workspace %s:/workspace:rw", hostPath)
	}
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
