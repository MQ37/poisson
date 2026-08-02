package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- ParseReadCall ---

// TestParseReadCall_ValidJSON: path/offset/limit all present and well-typed
// must come back verbatim with ok=true.
func TestParseReadCall_ValidJSON(t *testing.T) {
	path, offset, limit, ok := ParseReadCall(json.RawMessage(`{"path":"foo.go","offset":10,"limit":25}`))
	if !ok {
		t.Fatalf("ok = false, want true")
	}
	if path != "foo.go" || offset != 10 || limit != 25 {
		t.Errorf("got (%q, %d, %d), want (%q, %d, %d)", path, offset, limit, "foo.go", 10, 25)
	}
}

// TestParseReadCall_MalformedJSON: input that isn't valid JSON at all (and
// isn't rescued by the range-string fallback) must return ok=false.
func TestParseReadCall_MalformedJSON(t *testing.T) {
	_, _, _, ok := ParseReadCall(json.RawMessage(`{not json`))
	if ok {
		t.Fatalf("ok = true, want false for malformed JSON")
	}
}

// --- ReadWasTruncated ---

// truncatedFixture builds a temp file with n lines of body-length s.
func writeLines(t *testing.T, n, lineLen int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	var b strings.Builder
	line := strings.Repeat("x", lineLen)
	for i := 0; i < n; i++ {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func execRead(t *testing.T, path string) ToolResult {
	t.Helper()
	tool := NewReadTool(filepath.Dir(path), nil)
	input, _ := json.Marshal(map[string]string{"path": filepath.Base(path)})
	res, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return res
}

// TestReadWasTruncated_ByteCap: a file whose formatted output exceeds
// maxBytes (50KB) before the line count or a single line is the problem —
// Execute's byte-cap path ("output truncated at 50KB, N lines shown").
func TestReadWasTruncated_ByteCap(t *testing.T) {
	// 3000 lines of 50 chars each: ~55 bytes formatted per line, byte cap
	// (51200 bytes) trips around line ~930 — well before the 2000-line cap.
	path := writeLines(t, 3000, 50)
	res := execRead(t, path)

	if !strings.Contains(res.Content, "output truncated at 50KB") {
		t.Fatalf("Content missing byte-cap message; got tail: %q", tail(res.Content, 200))
	}
	if !ReadWasTruncated(res.Content) {
		t.Errorf("ReadWasTruncated = false, want true for byte-capped content")
	}
}

// TestReadWasTruncated_LineCap: a file with more than maxLines (2000) short
// lines, but well under maxBytes total — Execute's line-count-cap path
// ("output truncated at 2000 lines").
func TestReadWasTruncated_LineCap(t *testing.T) {
	// 3000 lines of 1 char each: ~5 bytes formatted per line * 3000 ≈ 15KB,
	// comfortably under the 50KB byte cap, so the 2000-line cap trips first.
	path := writeLines(t, 3000, 1)
	res := execRead(t, path)

	if !strings.Contains(res.Content, "output truncated at 2000 lines") {
		t.Fatalf("Content missing line-cap message; got tail: %q", tail(res.Content, 200))
	}
	if !ReadWasTruncated(res.Content) {
		t.Errorf("ReadWasTruncated = false, want true for line-capped content")
	}
}

// TestReadWasTruncated_LineTooLongCap: a single line far longer than
// maxLineSize (8MB) makes bufio.Scanner fail with ErrTooLong — Execute's
// third truncation path ("a line exceeded the read buffer").
func TestReadWasTruncated_LineTooLongCap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "long.txt")
	huge := strings.Repeat("a", 9*1024*1024) // > maxLineSize (8MB)
	if err := os.WriteFile(path, []byte(huge+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	res := execRead(t, path)

	if !strings.Contains(res.Content, "a line exceeded the read buffer") {
		t.Fatalf("Content missing line-too-long message; got tail: %q", tail(res.Content, 200))
	}
	if !ReadWasTruncated(res.Content) {
		t.Errorf("ReadWasTruncated = false, want true for a too-long-line read")
	}
}

// TestReadWasTruncated_OrdinaryContentFalse: a normal, un-truncated read
// must NOT be reported as truncated.
func TestReadWasTruncated_OrdinaryContentFalse(t *testing.T) {
	path := writeLines(t, 5, 10)
	res := execRead(t, path)

	if ReadWasTruncated(res.Content) {
		t.Errorf("ReadWasTruncated = true, want false for ordinary content: %q", res.Content)
	}
}

// TestReadWasTruncated_EmptyFileFalse: Execute's "(empty file)" placeholder
// is not a truncation and must not be reported as one.
func TestReadWasTruncated_EmptyFileFalse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	res := execRead(t, path)

	if res.Content != "(empty file)" {
		t.Fatalf("Content = %q, want %q", res.Content, "(empty file)")
	}
	if ReadWasTruncated(res.Content) {
		t.Errorf("ReadWasTruncated = true, want false for %q", res.Content)
	}
}

// --- ReadIsImage ---

// TestReadIsImage_ExactPrefixTrue pins the real prefix readImage emits
// ("Image: ", per read.go's fmt.Sprintf) as the only thing that counts.
func TestReadIsImage_ExactPrefixTrue(t *testing.T) {
	content := "Image: /tmp/foo.png (image/png, 1234 bytes) — see attached image."
	if !ReadIsImage(content) {
		t.Errorf("ReadIsImage(%q) = false, want true", content)
	}
}

// TestReadIsImage_SimilarLookingTextFalse: text that merely starts with
// "Image" (no colon+space) must not be mistaken for the image branch.
func TestReadIsImage_SimilarLookingTextFalse(t *testing.T) {
	for _, content := range []string{
		"Image processing complete",
		"Imagine: a file",
		"image: lowercase prefix",
		"1: Image: this is just line 1 of a text file\n",
		"",
	} {
		if ReadIsImage(content) {
			t.Errorf("ReadIsImage(%q) = true, want false", content)
		}
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
