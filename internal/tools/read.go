package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ReadTool reads the contents of a file.
type ReadTool struct {
	cwd string
}

func NewReadTool(cwd string) *ReadTool { return &ReadTool{cwd: cwd} }

func (t *ReadTool) Name() string { return "read" }

func (t *ReadTool) Description() string {
	return "Read the contents of a file. Supports text files and images (jpg, png, gif, webp). Output is truncated to 2000 lines or 50KB."
}

func (t *ReadTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": { "type": "string" },
    "offset": { "type": "integer", "description": "Line number to start reading from (1-indexed)" },
    "limit": { "type": "integer", "description": "Maximum number of lines to read" }
  },
  "required": ["path"]
}`)
}

type readInput struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

const (
	maxLines     = 2000
	maxBytes     = 50 * 1024
	readLineSize = 64 * 1024
)

func (t *ReadTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	var in readInput
	if err := json.Unmarshal(input, &in); err != nil {
		return ToolResult{Error: "invalid input: " + err.Error()}, nil
	}
	if in.Path == "" {
		return ToolResult{Error: "path is required"}, nil
	}

	path := resolvePath(t.cwd, in.Path)

	f, err := os.Open(path)
	if err != nil {
		return ToolResult{Error: "cannot open file: " + err.Error()}, nil
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, readLineSize), readLineSize)

	var b strings.Builder
	linesWritten := 0
	byteCount := 0
	lineNum := 0

	offset := in.Offset
	limit := in.Limit

	for scanner.Scan() {
		lineNum++
		if offset > 0 && lineNum < offset {
			continue
		}
		line := scanner.Text() + "\n"
		if byteCount+len(line) > maxBytes {
			remaining := maxBytes - byteCount
			if remaining > 0 {
				b.WriteString(utf8SafePrefix(line, remaining))
			}
			linesWritten++
			b.WriteString(fmt.Sprintf("\n... (output truncated at 50KB, %d lines shown)\n", linesWritten))
			break
		}
		b.WriteString(line)
		linesWritten++
		byteCount += len(line)

		if limit > 0 && linesWritten >= limit {
			break
		}
		if linesWritten >= maxLines {
			b.WriteString(fmt.Sprintf("\n... (output truncated at %d lines)\n", maxLines))
			break
		}
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return ToolResult{Error: "read error: " + err.Error()}, nil
	}

	content := b.String()
	if content == "" {
		content = "(empty file)"
	}
	return ToolResult{Content: content}, nil
}

// imageExtensions is the set of supported image extensions.
var imageExtensions = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true,
}

// isImagePath reports whether the path looks like an image file.
func isImagePath(path string) bool {
	return imageExtensions[strings.ToLower(filepath.Ext(path))]
}
