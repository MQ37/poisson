package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"poisson/internal/guard"
)

// EditTool edits a file using exact text replacement.
type EditTool struct {
	cwd        string
	sandbox    bool
	approvalFn ApprovalFn
}

func NewEditTool(cwd string, sandbox bool, approvalFn ApprovalFn) *EditTool {
	return &EditTool{cwd: cwd, sandbox: sandbox, approvalFn: approvalFn}
}

func (t *EditTool) Name() string { return "edit" }

func (t *EditTool) Description() string {
	return "Edit a file using exact text replacement. Every edits[].oldText must match a unique, non-overlapping region. For a single edit you can also pass oldText/newText directly at the top level instead of wrapping it in edits: [{...}]."
}

func (t *EditTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": { "type": "string" },
    "edits": {
      "type": "array",
      "description": "Use this for 2+ edits in one call. For a single edit, oldText/newText below are simpler.",
      "items": {
        "type": "object",
        "properties": {
          "oldText": { "type": "string" },
          "newText": { "type": "string" }
        },
        "required": ["oldText", "newText"]
      }
    },
    "oldText": { "type": "string", "description": "Shorthand for a single edit — use instead of edits: [{...}] when there's only one." },
    "newText": { "type": "string" }
  },
  "required": ["path"]
}`)
}

type editItem struct {
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
}

// rawEditInput mirrors the wire shape loosely: Edits is left raw so
// parseEditInput can recognize shapes beyond a strict array-of-objects —
// models calling this tool reliably reach for a flat top-level
// oldText/newText when there's only one edit, and occasionally double-encode
// the array as a JSON string. Both are handled below instead of just erroring.
type rawEditInput struct {
	Path    string          `json:"path"`
	Edits   json.RawMessage `json:"edits"`
	OldText string          `json:"oldText"`
	NewText string          `json:"newText"`
}

// parseEditInput accepts three shapes for the edit list: the documented
// edits: [{oldText, newText}, ...] array; a flat top-level oldText/newText
// pair as shorthand for a single edit (no array wrapper needed); and, best
// effort, an edits value that's itself a JSON-encoded string containing that
// array (some models double-encode it). Confirmed against real tool-call
// failures logged against this tool: a flat single-edit call used to
// unmarshal with an empty Edits slice and fail with the unhelpful "no edits
// provided", and a string-encoded edits value failed with a raw Go
// json.Unmarshal type-mismatch error that gave the model nothing to correct.
func parseEditInput(input json.RawMessage) (path string, edits []editItem, err error) {
	var raw rawEditInput
	if e := json.Unmarshal(input, &raw); e != nil {
		return "", nil, fmt.Errorf("invalid input: %w", e)
	}
	path = raw.Path
	if len(raw.Edits) == 0 || string(raw.Edits) == "null" {
		if raw.OldText != "" {
			return path, []editItem{{OldText: raw.OldText, NewText: raw.NewText}}, nil
		}
		return path, nil, nil
	}
	var items []editItem
	if e := json.Unmarshal(raw.Edits, &items); e == nil {
		return path, items, nil
	}
	var asString string
	if e := json.Unmarshal(raw.Edits, &asString); e == nil {
		if e2 := json.Unmarshal([]byte(asString), &items); e2 == nil {
			return path, items, nil
		}
	}
	return "", nil, fmt.Errorf("edits must be a JSON array of {oldText, newText} objects (e.g. edits: [{\"oldText\": \"...\", \"newText\": \"...\"}]), or oldText/newText directly for a single edit — not a JSON-encoded string")
}

func (t *EditTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	rawPath, edits, err := parseEditInput(input)
	if err != nil {
		return ToolResult{Error: err.Error()}, nil
	}
	if rawPath == "" {
		return ToolResult{Error: "path is required"}, nil
	}
	if len(edits) == 0 {
		return ToolResult{Error: "no edits provided — pass edits: [{oldText, newText}, ...], or oldText/newText directly for a single edit"}, nil
	}

	path := resolvePath(t.cwd, rawPath)

	if !t.sandbox {
		if reason := guard.SensitivePathReason(path); reason != "" {
			allowed, denyReason := t.approvalFn(ctx, "edit "+path, reason, t.cwd)
			if !allowed {
				return ToolResult{Error: sensitivePathDenyMsg(reason, denyReason)}, nil
			}
		}
	}

	info, err := os.Stat(path)
	if err != nil {
		return ToolResult{Error: "cannot stat file: " + err.Error()}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ToolResult{Error: "cannot read file: " + err.Error()}, nil
	}
	original := string(data)

	type replacement struct {
		start int
		end   int
		text  string
		idx   int
	}
	repls := make([]replacement, 0, len(edits))
	for i, e := range edits {
		if e.OldText == "" {
			return ToolResult{Error: fmt.Sprintf("edit %d: oldText is empty", i)}, nil
		}
		count := strings.Count(original, e.OldText)
		if count == 0 {
			return ToolResult{Error: fmt.Sprintf("edit %d: oldText not found in file", i)}, nil
		}
		if count > 1 {
			return ToolResult{Error: fmt.Sprintf("edit %d: oldText is not unique (%d matches)", i, count)}, nil
		}
		start := strings.Index(original, e.OldText)
		repls = append(repls, replacement{start: start, end: start + len(e.OldText), text: e.NewText, idx: i})
	}
	sort.Slice(repls, func(i, j int) bool { return repls[i].start < repls[j].start })
	for i := 1; i < len(repls); i++ {
		if repls[i].start < repls[i-1].end {
			return ToolResult{Error: fmt.Sprintf("edit %d overlaps edit %d", repls[i].idx, repls[i-1].idx)}, nil
		}
	}

	var b strings.Builder
	pos := 0
	for _, r := range repls {
		b.WriteString(original[pos:r.start])
		b.WriteString(r.text)
		pos = r.end
	}
	b.WriteString(original[pos:])

	if err := os.WriteFile(path, []byte(b.String()), info.Mode().Perm()); err != nil {
		return ToolResult{Error: "cannot write file: " + err.Error()}, nil
	}

	return ToolResult{Content: fmt.Sprintf("edited %s (%d edit(s) applied)", path, len(edits))}, nil
}
