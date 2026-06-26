package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
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
	repls := make([]replacement, 0, len(in.Edits))
	for i, e := range in.Edits {
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

	return ToolResult{Content: fmt.Sprintf("edited %s (%d edit(s) applied)", path, len(in.Edits))}, nil
}
