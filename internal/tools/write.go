package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
)

// WriteTool writes content to a file, creating parent directories.
type WriteTool struct {
	cwd string
}

func NewWriteTool(cwd string) *WriteTool { return &WriteTool{cwd: cwd} }

func (t *WriteTool) Name() string { return "write" }

func (t *WriteTool) Description() string {
	return "Write content to a file. Creates parent directories. Overwrites if exists."
}

func (t *WriteTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": { "type": "string" },
    "content": { "type": "string" }
  },
  "required": ["path", "content"]
}`)
}

type writeInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (t *WriteTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	var in writeInput
	if err := json.Unmarshal(input, &in); err != nil {
		return ToolResult{Error: "invalid input: " + err.Error()}, nil
	}
	if in.Path == "" {
		return ToolResult{Error: "path is required"}, nil
	}

	path := resolvePath(t.cwd, in.Path)

	// Create parent directories.
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return ToolResult{Error: "cannot create directories: " + err.Error()}, nil
		}
	}

	if err := os.WriteFile(path, []byte(in.Content), 0o644); err != nil {
		return ToolResult{Error: "cannot write file: " + err.Error()}, nil
	}

	return ToolResult{Content: "wrote " + path}, nil
}
