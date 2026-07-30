package tools

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/mq37/poisson/internal/sandbox"
)

// ListSandboxesTool browses every live poisson sandbox container on this
// host, not just ones the current session created — the discovery half of
// crash recovery (see docs/sandbox-plan.md's "Crash recovery" section):
// find a sandbox by the name it was given, whether this session created it,
// a different one did, or the process that created it has since crashed.
// Read-only: listing never grants access on its own, Manager.Get/Owns still
// gate bash/sandbox_cp/sandbox_destroy the same way regardless.
type ListSandboxesTool struct {
	mgr *sandbox.Manager
}

func NewListSandboxesTool(mgr *sandbox.Manager) *ListSandboxesTool {
	return &ListSandboxesTool{mgr: mgr}
}

func (t *ListSandboxesTool) Name() string { return "list_sandboxes" }

func (t *ListSandboxesTool) Description() string {
	return "List every live podman sandbox container on this host, across every session and process — including ones from a crashed or otherwise-finished session. Use it to find an existing sandbox by name (its sandboxId) before creating a new one, especially after a crash or to check what another concurrent session is already using. Each entry shows sandboxId, hostPath, which session created it, when, and whether it's still running. This only browses — pass sandboxId to bash/sandbox_cp/sandbox_destroy to actually use one, and prefer sandboxes you recognize as your own over ones that look like someone else's active work."
}

func (t *ListSandboxesTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type": "object", "properties": {}}`)
}

type sandboxListEntry struct {
	SandboxID string `json:"sandboxId"`
	HostPath  string `json:"hostPath"`
	SessionID string `json:"sessionId,omitempty"`
	CreatedAt string `json:"createdAt,omitempty"`
	Running   bool   `json:"running"`
}

func (t *ListSandboxesTool) Execute(ctx context.Context, _ json.RawMessage) (ToolResult, error) {
	if t.mgr == nil {
		return ToolResult{Error: "sandboxing is not available in this session"}, nil
	}
	infos, err := t.mgr.List(ctx)
	if err != nil {
		return ToolResult{Error: "list sandboxes: " + err.Error()}, nil
	}
	if len(infos) == 0 {
		return ToolResult{Content: "no live sandboxes"}, nil
	}

	entries := make([]sandboxListEntry, 0, len(infos))
	for _, info := range infos {
		e := sandboxListEntry{SandboxID: info.ID, HostPath: info.HostPath, SessionID: info.SessionID, Running: info.Running}
		if !info.CreatedAt.IsZero() {
			e.CreatedAt = info.CreatedAt.Format("2006-01-02T15:04:05Z07:00")
		}
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].SandboxID < entries[j].SandboxID })

	data, _ := json.Marshal(entries)
	return ToolResult{Content: string(data)}, nil
}
