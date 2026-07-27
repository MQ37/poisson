package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mq37/poisson/internal/imaging"
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

// parseReadInput unmarshals input into readInput, tolerating a single
// offset/limit field carrying a "START, END" (or "START-END") line-range
// string instead of a plain integer — a common slip when the model means
// "read lines 80 to 130" but only fills in one field. The schema still
// declares offset/limit as separate integers; this only rescues that one
// bad shape (FlexInt's own error) instead of hard-failing the whole call.
func parseReadInput(data []byte) (readInput, error) {
	var in readInput
	unmarshalErr := json.Unmarshal(data, &in)
	if unmarshalErr == nil {
		return in, nil
	}

	var raw struct {
		Path   string          `json:"path"`
		Offset json.RawMessage `json:"offset"`
		Limit  json.RawMessage `json:"limit"`
	}
	if json.Unmarshal(data, &raw) != nil {
		return readInput{}, unmarshalErr
	}
	start, end, ok := rangeFromRaw(raw.Offset)
	if !ok {
		start, end, ok = rangeFromRaw(raw.Limit)
	}
	if !ok {
		return readInput{}, unmarshalErr
	}
	in.Path = raw.Path
	in.Offset = FlexInt(start)
	if end > start {
		in.Limit = FlexInt(end - start + 1)
	}
	return in, nil
}

// ParseReadCall parses a read tool_use input the same lenient way the read
// tool itself does (parseReadInput), including a range-shaped offset/limit.
// Callers outside this package that reason about which lines a read call
// covers (agent read memoization, compaction pruning) must go through this
// so their view matches what the tool actually read. ok is false for input
// the tool would reject too.
func ParseReadCall(input json.RawMessage) (path string, offset, limit int, ok bool) {
	in, err := parseReadInput(input)
	if err != nil {
		return "", 0, 0, false
	}
	return in.Path, int(in.Offset), int(in.Limit), true
}

// rangeFromRaw extracts "START, END" / "START-END" from a raw JSON string
// value. ok is false for anything else (plain number, absent field, or a
// string that isn't a two-number range) — those already parse as FlexInt.
func rangeFromRaw(raw json.RawMessage) (start, end int, ok bool) {
	if len(raw) == 0 {
		return 0, 0, false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return 0, 0, false
	}
	sep := ","
	if !strings.Contains(s, sep) {
		sep = "-"
	}
	parts := strings.SplitN(s, sep, 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	lo, errLo := strconv.Atoi(strings.TrimSpace(parts[0]))
	hi, errHi := strconv.Atoi(strings.TrimSpace(parts[1]))
	if errLo != nil || errHi != nil || lo <= 0 || hi < lo {
		return 0, 0, false
	}
	return lo, hi, true
}

const (
	maxLines      = 2000
	maxBytes      = 50 * 1024
	maxImageBytes = 5 * 1024 * 1024
	readLineSize  = 64 * 1024       // initial per-line scan buffer
	maxLineSize   = 8 * 1024 * 1024 // max single line before scanning gives up
)

func (t *ReadTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	in, err := parseReadInput(input)
	if err != nil {
		return ToolResult{Error: "invalid input: " + err.Error()}, nil
	}
	if in.Path == "" {
		return ToolResult{Error: "path is required"}, nil
	}

	path := resolvePath(t.cwd, in.Path)

	if res, ok := checkSensitivePath(ctx, t.cwd, t.sandbox, "read", path, t.approvalFn); !ok {
		return res, nil
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

// readImage loads an image file for the model to actually see. It never
// inlines base64 into Content (see ToolResult's doc comment for why) —
// instead it downscales/re-encodes via imaging.ProcessFile (the same
// pipeline a pasted/attached image goes through: capped at 1024px long
// edge, re-encoded as PNG) and returns the resulting temp path. The agent
// turns that into a sibling "image" content block next to the tool_result;
// every provider already knows how to load + encode one, since that's
// exactly how a user-attached image reaches them.
func (t *ReadTool) readImage(path string) (ToolResult, error) {
	info, err := os.Stat(path)
	if err != nil {
		return ToolResult{Error: "cannot stat file: " + err.Error()}, nil
	}
	if info.Size() > maxImageBytes {
		return ToolResult{Error: fmt.Sprintf("image too large (%d bytes, max %d)", info.Size(), maxImageBytes)}, nil
	}
	outPath, mediaType, err := imaging.ProcessFile(path)
	if err != nil {
		return ToolResult{Error: "cannot read image: " + err.Error()}, nil
	}
	content := fmt.Sprintf("Image: %s (%s, %d bytes) — see attached image.", path, imageMIME(path), info.Size())
	return ToolResult{
		Content:   content,
		ImagePath: outPath,
		MediaType: mediaType,
		ImageName: filepath.Base(path),
	}, nil
}
