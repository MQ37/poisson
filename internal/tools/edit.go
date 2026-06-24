package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// EditTool edits a file using exact text replacement.
type EditTool struct {
	cwd string
}

func NewEditTool(cwd string) *EditTool { return &EditTool{cwd: cwd} }

func (t *EditTool) Name() string { return "edit" }

func (t *EditTool) Description() string {
	return "Edit a file using exact text replacement. Each oldText must match a unique, non-overlapping region."
}

func (t *EditTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": { "type": "string" },
    "edits": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "oldText": { "type": "string" },
          "newText": { "type": "string" }
        },
        "required": ["oldText", "newText"]
      }
    }
  },
  "required": ["path", "edits"]
}`)
}

type editItem struct {
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
}

type editInput struct {
	Path  string     `json:"path"`
	Edits []editItem `json:"edits"`
}

func (t *EditTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	var in editInput
	if err := json.Unmarshal(input, &in); err != nil {
		return ToolResult{Error: "invalid input: " + err.Error()}, nil
	}
	if in.Path == "" {
		return ToolResult{Error: "path is required"}, nil
	}
	if len(in.Edits) == 0 {
		return ToolResult{Error: "no edits provided"}, nil
	}

	path := resolvePath(t.cwd, in.Path)

	data, err := os.ReadFile(path)
	if err != nil {
		return ToolResult{Error: "cannot read file: " + err.Error()}, nil
	}
	content := string(data)

	// Apply each edit, verifying uniqueness.
	for i, e := range in.Edits {
		if e.OldText == "" {
			return ToolResult{Error: fmt.Sprintf("edit %d: oldText is empty", i)}, nil
		}
		count := strings.Count(content, e.OldText)
		if count == 0 {
			return ToolResult{Error: fmt.Sprintf("edit %d: oldText not found in file", i)}, nil
		}
		if count > 1 {
			return ToolResult{Error: fmt.Sprintf("edit %d: oldText is not unique (%d matches)", i, count)}, nil
		}
		content = strings.Replace(content, e.OldText, e.NewText, 1)
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return ToolResult{Error: "cannot write file: " + err.Error()}, nil
	}

	return ToolResult{Content: fmt.Sprintf("edited %s (%d edit(s) applied)", path, len(in.Edits))}, nil
}
