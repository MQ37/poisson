package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mq37/poisson/internal/guard"
	"github.com/mq37/poisson/internal/sandbox"
)

// SandboxCpTool moves files between an arbitrary host path and a sandbox's
// own workspace mount. Nothing here needs the Manager's Driver at all — a
// sandbox's workspace (Sandbox.HostPath) is already a plain host directory
// (see docs/sandbox-plan.md's "File tools" / bind-mount design), so this is
// an ordinary host-to-host file copy, not a podman operation. It exists
// specifically for moving things beyond the base workspace mount (pulling
// in a second project directory, extracting a build artifact elsewhere) —
// anything already inside the workspace is already reachable directly via
// read/write/edit/grep/glob against hostPath, no sandbox_cp needed.
type SandboxCpTool struct {
	cwd        string
	mgr        *sandbox.Manager
	approvalFn ApprovalFn // same sensitive-path gate read/write use
}

func NewSandboxCpTool(cwd string, mgr *sandbox.Manager, approvalFn ApprovalFn) *SandboxCpTool {
	return &SandboxCpTool{cwd: cwd, mgr: mgr, approvalFn: approvalFn}
}

func (t *SandboxCpTool) Name() string { return "sandbox_cp" }

func (t *SandboxCpTool) Description() string {
	return "Copy a file between an arbitrary host path and a sandbox's own workspace. direction \"in\" copies hostPath -> the sandbox's workspacePath; \"out\" copies the sandbox's workspacePath -> hostPath. Only for moving things beyond the base workspace mount — files already under the sandbox's own hostPath (from create_sandbox) are already reachable directly via read/write/edit/grep/glob, no sandbox_cp needed for those. hostPath is gated the same as read/write (sensitive-path approval); workspacePath can never escape the sandbox's own workspace root."
}

func (t *SandboxCpTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "sandboxId": { "type": "string" },
    "direction": { "type": "string", "enum": ["in", "out"], "description": "\"in\": hostPath -> sandbox workspace. \"out\": sandbox workspace -> hostPath." },
    "hostPath": { "type": "string", "description": "Arbitrary host path (absolute, or relative to session cwd)" },
    "workspacePath": { "type": "string", "description": "Path relative to the sandbox's own workspace root (an absolute-looking path is still treated as workspace-relative, never a literal host path)" }
  },
  "required": ["sandboxId", "direction", "hostPath", "workspacePath"]
}`)
}

type sandboxCpInput struct {
	SandboxID     string `json:"sandboxId"`
	Direction     string `json:"direction"`
	HostPath      string `json:"hostPath"`
	WorkspacePath string `json:"workspacePath"`
}

func (t *SandboxCpTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	if t.mgr == nil {
		return ToolResult{Error: "sandboxing is not available in this session"}, nil
	}
	var in sandboxCpInput
	if err := json.Unmarshal(input, &in); err != nil {
		return ToolResult{Error: "invalid input: " + err.Error()}, nil
	}
	if in.SandboxID == "" || in.HostPath == "" || in.WorkspacePath == "" {
		return ToolResult{Error: "sandboxId, hostPath, and workspacePath are all required"}, nil
	}
	if in.Direction != "in" && in.Direction != "out" {
		return ToolResult{Error: `direction must be "in" or "out"`}, nil
	}

	sb, ok := t.mgr.Get(in.SandboxID)
	if !ok {
		return ToolResult{Error: fmt.Sprintf("sandbox %q not found — it may belong to a different session, have been destroyed, or never existed", in.SandboxID)}, nil
	}

	wsPath, err := resolveInWorkspace(sb.HostPath, in.WorkspacePath)
	if err != nil {
		return ToolResult{Error: err.Error()}, nil
	}
	hostPath := resolvePath(t.cwd, in.HostPath)

	// hostPath is the one side of every copy that's a real, arbitrary host
	// location — gate it exactly like read/write do, regardless of
	// direction (reading FROM it or writing TO it is equally sensitive).
	if res, ok := checkSensitivePath(ctx, t.cwd, "sandbox_cp "+in.Direction, hostPath, t.approvalFn); !ok {
		return res, nil
	}

	var src, dst string
	if in.Direction == "in" {
		src, dst = hostPath, wsPath
	} else {
		src, dst = wsPath, hostPath
	}

	data, err := os.ReadFile(src)
	if err != nil {
		return ToolResult{Error: "cannot read source: " + err.Error()}, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return ToolResult{Error: "cannot create destination directory: " + err.Error()}, nil
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(dst); err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.WriteFile(dst, data, mode); err != nil {
		return ToolResult{Error: "cannot write destination: " + err.Error()}, nil
	}

	return ToolResult{Content: fmt.Sprintf("copied %s -> %s (%d bytes)", src, dst, len(data))}, nil
}

// resolveInWorkspace resolves p (a model-supplied path, possibly
// absolute-looking) against root (a sandbox's own workspace hostPath) and
// verifies the result can't escape root — an absolute p is deliberately
// treated as workspace-relative, never as a literal host path: without
// this, "workspacePath": "/etc/passwd" would resolve to the real host
// /etc/passwd instead of the harmless path an agent thinking in
// container-relative terms would expect. Symlinks are resolved on both
// sides (guard.ResolveSymlinkTarget) so a symlink planted inside the
// workspace can't point the copy somewhere outside it either.
func resolveInWorkspace(root, p string) (string, error) {
	rel := strings.TrimPrefix(filepath.Clean("/"+p), "/")
	full := filepath.Join(root, rel)

	resolvedFull := guard.ResolveSymlinkTarget(full)
	resolvedRoot := guard.ResolveSymlinkTarget(root)
	if resolvedFull != resolvedRoot && !strings.HasPrefix(resolvedFull, resolvedRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("workspacePath %q escapes the sandbox workspace", p)
	}
	return full, nil
}
