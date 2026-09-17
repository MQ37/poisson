package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mq37/poisson/internal/sandbox"
)

// SandboxTool is the single model-facing entry point for every sandbox
// operation, dispatching on its "action" field to one of the five
// underlying tool types (CreateSandboxTool, SandboxCpTool,
// SandboxDestroyTool, SandboxResurrectTool, ListSandboxesTool) below it.
// Those five keep their own exported types, constructors, and test files
// completely unchanged — this wrapper only replaces how they're exposed to
// the model, cutting five tool name+description+schema blocks down to one
// (see HarnessTax's finding that first-call context is dominated by tool
// schema/instruction bulk, not turn count: https://harnesstax.github.io/).
//
// allowCreate mirrors the old registration-time omission of create_sandbox
// from a child (subagent) registry: a subagent must never mint its own
// sandbox, only use ones its parent explicitly authorized. Since all five
// actions now share one tool name, that restriction can no longer be
// expressed by simply not registering a tool — instead action=create is
// dropped from this instance's schema enum (so a child's model never even
// sees it as an option) and Execute rejects it defensively regardless.
type SandboxTool struct {
	create      *CreateSandboxTool
	cp          *SandboxCpTool
	destroy     *SandboxDestroyTool
	resurrect   *SandboxResurrectTool
	list        *ListSandboxesTool
	allowCreate bool
	schema      json.RawMessage
}

// NewSandboxTool builds the unified sandbox tool. sandboxApprovalFn gates
// create's mount/env/hostPath requests (see CreateSandboxTool); fileApprovalFn
// gates cp's sensitive hostPath side (see SandboxCpTool) — the same split
// BuildRegistry already made when these were five separate registrations.
func NewSandboxTool(cwd string, mgr *sandbox.Manager, sandboxApprovalFn, fileApprovalFn ApprovalFn, allowCreate bool) *SandboxTool {
	return &SandboxTool{
		create:      NewCreateSandboxTool(cwd, mgr, sandboxApprovalFn),
		cp:          NewSandboxCpTool(cwd, mgr, fileApprovalFn),
		destroy:     NewSandboxDestroyTool(mgr),
		resurrect:   NewSandboxResurrectTool(mgr),
		list:        NewListSandboxesTool(mgr),
		allowCreate: allowCreate,
		schema:      buildSandboxSchema(allowCreate),
	}
}

func (t *SandboxTool) Name() string { return "sandbox" }

func (t *SandboxTool) Description() string {
	return "Manage podman sandbox containers via action: \"create\" (new isolated container, returns {sandboxId, hostPath}), \"cp\" (copy a file/directory between an arbitrary host path and a sandbox's own workspace), \"destroy\" (kill a container — never touches its hostPath/mounts), \"resurrect\" (resume a stopped container, keeps its state), \"list\" (browse every sandbox on this host, running or stopped, across every session and process).\n\n" +
		"create: no default workspace — pass hostPath to bind-mount a directory as /workspace, or omit for an isolated container with none; hostPath is used as-is, never copied. Give it a descriptive name (e.g. \"api-testing-2\") — becomes its sandboxId (prefixed px-sandbox-), reusable by any session later via action=list. A name already in use fails clearly; check action=list first, or use action=resurrect if it shows running=false. Requesting hostPath, extra mounts, or env needs human approval, showing the exact paths; a plain create with none of those does not.\n\n" +
		"cp: direction \"in\" copies hostPath -> workspacePath inside the sandbox, \"out\" the reverse. Only for moving things beyond the base workspace mount — files already under a sandbox's own hostPath are reachable directly via read/write/edit/grep/glob, no cp needed for those. hostPath is gated like read/write (sensitive-path approval); workspacePath can never escape the sandbox's own root. Symlinks inside a copied directory are skipped, never followed.\n\n" +
		"destroy: only discards the container, never any host directory mounted into it. resurrect: safe no-op if already running; never recreates or loses container state (packages, files outside /workspace, shell history all persist) — use create instead for a genuinely fresh one. list: every entry shows sandboxId, hostPath, owning session, and running state; a stopped one still exists with its state intact.\n\n" +
		"Pass sandboxId to bash's own sandboxId param to run commands inside a sandbox created here."
}

func (t *SandboxTool) Schema() json.RawMessage { return t.schema }

// buildSandboxSchema produces the schema once at construction time — the
// action enum is the only part that varies (allowCreate strips "create"),
// so this isn't recomputed per call.
func buildSandboxSchema(allowCreate bool) json.RawMessage {
	actions := []string{"cp", "destroy", "resurrect", "list"}
	if allowCreate {
		actions = append([]string{"create"}, actions...)
	}
	actionsJSON, _ := json.Marshal(actions)
	return json.RawMessage(fmt.Sprintf(`{
  "type": "object",
  "properties": {
    "action": { "type": "string", "enum": %s, "description": "Which sandbox operation to perform." },
    "image": { "type": "string", "description": "create: container image (default: a pinned Ubuntu LTS)." },
    "name": { "type": "string", "description": "create: descriptive name (e.g. \"api-testing-2\") — becomes its sandboxId, prefixed px-sandbox-. Omit for a random name. Must be unique across every session on this host." },
    "hostPath": { "type": "string", "description": "create: host directory to bind-mount as /workspace (omit for none). cp: arbitrary host path (absolute, or relative to session cwd)." },
    "mounts": {
      "type": "array",
      "description": "create: extra host bind mounts beyond the base workspace — requires human approval, showing the exact host paths.",
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
      "description": "create: extra KEY=VALUE entries injected into the container — requires human approval."
    },
    "sandboxId": { "type": "string", "description": "cp/destroy/resurrect: the sandbox to act on." },
    "direction": { "type": "string", "enum": ["in", "out"], "description": "cp: \"in\" copies hostPath -> workspacePath, \"out\" the reverse." },
    "workspacePath": { "type": "string", "description": "cp: path relative to the sandbox's own workspace root." }
  },
  "required": ["action"]
}`, actionsJSON))
}

func (t *SandboxTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	var in struct {
		Action string `json:"action"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &in); err != nil {
			return ToolResult{Error: "invalid input: " + err.Error()}, nil
		}
	}
	switch in.Action {
	case "create":
		if !t.allowCreate {
			return ToolResult{Error: `action "create" is not available in this session`}, nil
		}
		return t.create.Execute(ctx, input)
	case "cp":
		return t.cp.Execute(ctx, input)
	case "destroy":
		return t.destroy.Execute(ctx, input)
	case "resurrect":
		return t.resurrect.Execute(ctx, input)
	case "list":
		return t.list.Execute(ctx, input)
	case "":
		return ToolResult{Error: `action is required: one of "create", "cp", "destroy", "resurrect", "list"`}, nil
	default:
		return ToolResult{Error: fmt.Sprintf("unknown action %q: must be one of create, cp, destroy, resurrect, list", in.Action)}, nil
	}
}
