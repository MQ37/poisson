package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
)

// WriteTool writes content to a file, creating parent directories.
type WriteTool struct {
	cwd        string
	sandbox    bool
	approvalFn ApprovalFn
}

func NewWriteTool(cwd string, sandbox bool, approvalFn ApprovalFn) *WriteTool {
	return &WriteTool{cwd: cwd, sandbox: sandbox, approvalFn: approvalFn}
}

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

	if res, ok := checkSensitivePath(ctx, t.cwd, t.sandbox, "write", path, t.approvalFn); !ok {
		return res, nil
	}

	// Create parent directories.
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return ToolResult{Error: "cannot create directories: " + err.Error()}, nil
		}
	}

	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.WriteFile(path, []byte(in.Content), mode); err != nil {
		return ToolResult{Error: "cannot write file: " + err.Error()}, nil
	}

	return ToolResult{Content: "wrote " + path}, nil
}
