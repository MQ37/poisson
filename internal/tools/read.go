package tools

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mq37/poisson/internal/guard"
)

// ReadTool reads the contents of a file.
type ReadTool struct {
	cwd        string
	sandbox    bool
	approvalFn ApprovalFn
}

func NewReadTool(cwd string, sandbox bool, approvalFn ApprovalFn) *ReadTool {
	return &ReadTool{cwd: cwd, sandbox: sandbox, approvalFn: approvalFn}
}

func (t *ReadTool) Name() string { return "read" }

func (t *ReadTool) Description() string {
	return "Read the contents of a file. Output is prefixed with line numbers (N: text). Supports text files and images (jpg, png, gif, webp). offset/limit read a line range, like `sed -n 'START,ENDp'`. Output is truncated to 2000 lines or 50KB. Prefer this over bash `cat`/`head`/`tail`/`sed -n`/`cat -n` when reading the file (or a line range) is the whole command — skips bash's risk-classification step entirely. For a SKILL.md under ~/.poisson/skills/, use the `skill` tool instead — don't read it directly."
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
	Path   string  `json:"path"`
	Offset FlexInt `json:"offset"`
	Limit  FlexInt `json:"limit"`
}

const (
	maxLines      = 2000
	maxBytes      = 50 * 1024
	maxImageBytes = 5 * 1024 * 1024
	readLineSize  = 64 * 1024       // initial per-line scan buffer
	maxLineSize   = 8 * 1024 * 1024 // max single line before scanning gives up
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

	if !t.sandbox {
		if reason := guard.SensitivePathReason(path); reason != "" {
			allowed, denyReason := t.approvalFn(ctx, "read "+path, reason, t.cwd)
			if !allowed {
				return ToolResult{Error: sensitivePathDenyMsg(reason, denyReason)}, nil
			}
		}
	}

	if isImagePath(path) {
		return t.readImage(path)
	}

	f, err := os.Open(path)
	if err != nil {
		return ToolResult{Error: "cannot open file: " + err.Error()}, nil
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, readLineSize), maxLineSize)

	var b strings.Builder
	linesWritten := 0
	byteCount := 0
	lineNum := 0

	offset := int(in.Offset)
	limit := int(in.Limit)

	for scanner.Scan() {
		lineNum++
		if offset > 0 && lineNum < offset {
			continue
		}
		// Numbered like search's own "path:N:" output — the model consistently
		// wants line numbers to target a later edit (it adds grep's -n to nearly
		// every search call), and plain read had no equivalent at all.
		line := fmt.Sprintf("%d: %s\n", lineNum, scanner.Text())
		if byteCount+len(line) > maxBytes {
			remaining := maxBytes - byteCount
			if remaining > 0 {
				b.WriteString(utf8SafePrefix(line, remaining))
				linesWritten++
			}
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
		// A single line longer than maxLineSize (e.g. a minified bundle) would
		// otherwise fail the whole read; return what we have with a note instead.
		if err == bufio.ErrTooLong {
			b.WriteString("\n... (a line exceeded the read buffer; output truncated)\n")
		} else {
			return ToolResult{Error: "read error: " + err.Error()}, nil
		}
	}

	content := b.String()
	if content == "" {
		content = "(empty file)"
	}
	return ToolResult{Content: content}, nil
}

// ReadWasTruncated reports whether a read tool's returned content was cut
// short by one of its own caps (maxLines/maxBytes/maxLineSize) rather than
// ending because the file did. Callers that want to know whether a read
// covers a file "to EOF" (e.g. agent-level read memoization, deciding
// whether a later unbounded read is safely covered by this one) must check
// this — an unbounded request (limit=0) that got truncated did NOT actually
// see the rest of the file, regardless of what was asked for.
func ReadWasTruncated(content string) bool {
	return strings.Contains(content, "truncated")
}

// ReadIsImage reports whether a read tool's returned content is the
// image/base64 branch (readImage) rather than plain text. Image reads carry
// no line-range semantics, so callers doing line-range memoization must
// exclude them.
func ReadIsImage(content string) bool {
	return strings.HasPrefix(content, "Image: ")
}

// imageExtensions is the set of supported image extensions.
var imageExtensions = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true,
}

// isImagePath reports whether the path looks like an image file.
func isImagePath(path string) bool {
	return imageExtensions[strings.ToLower(filepath.Ext(path))]
}

func imageMIME(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return "application/octet-stream"
	}
}

func (t *ReadTool) readImage(path string) (ToolResult, error) {
	info, err := os.Stat(path)
	if err != nil {
		return ToolResult{Error: "cannot stat file: " + err.Error()}, nil
	}
	if info.Size() > maxImageBytes {
		return ToolResult{Error: fmt.Sprintf("image too large (%d bytes, max %d)", info.Size(), maxImageBytes)}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ToolResult{Error: "cannot read image: " + err.Error()}, nil
	}
	mime := imageMIME(path)
	b64 := base64.StdEncoding.EncodeToString(data)
	content := fmt.Sprintf("Image: %s (%s, %d bytes)\nbase64:\n%s", path, mime, len(data), b64)
	return ToolResult{Content: content}, nil
}
