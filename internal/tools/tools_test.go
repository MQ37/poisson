package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/testutil"
)

func mustJSON(t *testing.T, v interface{}) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

// stubTool is a minimal Tool for registry ordering tests.
type stubTool struct{ name string }

func (s stubTool) Name() string            { return s.name }
func (s stubTool) Description() string     { return s.name + " desc" }
func (s stubTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (s stubTool) Execute(context.Context, json.RawMessage) (ToolResult, error) {
	return ToolResult{}, nil
}

// TestDefinitionsStableSortedOrder guards the prompt-cache fix: Definitions()
// must return a byte-stable, sorted order. Ranging the tools map is randomized,
// which reshuffled the tools array (and the tool-name list in the system
// prompt) every request and broke Anthropic caching (cache_read stayed 0).
func TestDefinitionsStableSortedOrder(t *testing.T) {
	r := NewRegistry()
	for _, n := range []string{"read", "write", "bash", "edit", "ls", "glob"} {
		r.Register(stubTool{name: n})
	}
	want := []string{"bash", "edit", "glob", "ls", "read", "write"}
	for i := 0; i < 50; i++ {
		defs := r.Definitions()
		got := make([]string, len(defs))
		for j, d := range defs {
			got[j] = d.Name
		}
		for k := range want {
			if got[k] != want[k] {
				t.Fatalf("iteration %d: order = %v, want sorted %v", i, got, want)
			}
		}
	}
}

func TestWriteThenRead(t *testing.T) {
	dir := testutil.TempDir(t)
	w := NewWriteTool(dir, true, nil)
	r := NewReadTool(dir, true, nil)

	path := "hello.txt"
	content := "line1\nline2\nline3\n"

	res, err := w.Execute(context.Background(), mustJSON(t, map[string]string{"path": path, "content": content}))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("write error: %s", res.Error)
	}

	// Verify on disk.
	got, err := os.ReadFile(filepath.Join(dir, path))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(got) != content {
		t.Errorf("file content = %q, want %q", got, content)
	}

	// Read via tool.
	res, err = r.Execute(context.Background(), mustJSON(t, map[string]string{"path": path}))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("read tool error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "line1") || !strings.Contains(res.Content, "line3") {
		t.Errorf("read content = %q, expected all lines", res.Content)
	}
}

func TestRead_LongSingleLine(t *testing.T) {
	dir := testutil.TempDir(t)
	r := NewReadTool(dir, true, nil)
	// A single line larger than the initial scan buffer (64KB) but under the
	// giving-up cap — e.g. a minified bundle. Must not hard-fail.
	line := strings.Repeat("x", 200*1024)
	if err := os.WriteFile(filepath.Join(dir, "min.js"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := r.Execute(context.Background(), mustJSON(t, map[string]string{"path": "min.js"}))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("long line should not error, got: %s", res.Error)
	}
	if !strings.Contains(res.Content, "x") {
		t.Errorf("expected file content, got %q", res.Content)
	}
	if len(res.Content) > maxBytes+1024 {
		t.Errorf("content %d bytes exceeds cap", len(res.Content))
	}
}

// TestRead_LinesAreNumbered is the reported gap: models kept falling back to
// bash `cat -n`/`grep -n` because plain read had no line numbers at all,
// unlike search's own "path:N:" output. Numbers must reflect the real file
// line, not a renumbered index starting at 1 (offset/limit is checked
// separately in TestRead_OffsetLimit).
func TestRead_LinesAreNumbered(t *testing.T) {
	dir := testutil.TempDir(t)
	w := NewWriteTool(dir, true, nil)
	r := NewReadTool(dir, true, nil)
	w.Execute(context.Background(), mustJSON(t, map[string]string{"path": "f.txt", "content": "alpha\nbeta\ngamma\n"}))

	res, _ := r.Execute(context.Background(), mustJSON(t, map[string]string{"path": "f.txt"}))
	if res.Error != "" {
		t.Fatalf("read error: %s", res.Error)
	}
	want := "1: alpha\n2: beta\n3: gamma\n"
	if res.Content != want {
		t.Fatalf("content = %q, want %q", res.Content, want)
	}
}

func TestRead_ImageBase64(t *testing.T) {
	dir := testutil.TempDir(t)
	path := filepath.Join(dir, "pixel.png")
	// Minimal 1x1 PNG
	png := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
		0x89, 0x00, 0x00, 0x00, 0x0a, 0x49, 0x44, 0x41,
		0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00,
		0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
		0x42, 0x60, 0x82,
	}
	if err := os.WriteFile(path, png, 0644); err != nil {
		t.Fatal(err)
	}
	r := NewReadTool(dir, true, nil)
	res, err := r.Execute(context.Background(), mustJSON(t, map[string]string{"path": "pixel.png"}))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("read error: %s", res.Error)
	}
	// Content is a short description only — the actual image is never
	// inlined as base64 text (it used to be, and silently corrupted past
	// maxToolOutputBytes; see ToolResult's doc comment). Real image data
	// travels via ImagePath/MediaType instead, the same convention as a
	// user-attached image, loaded by the provider at request-build time.
	if !strings.Contains(res.Content, "image/png") {
		t.Fatalf("expected image metadata in Content, got %q", res.Content)
	}
	if strings.Contains(res.Content, "base64:") {
		t.Fatalf("Content must not inline base64 data anymore, got %q", res.Content)
	}
	if res.ImagePath == "" {
		t.Fatalf("expected ImagePath set for an image read")
	}
	if res.MediaType != "image/png" {
		t.Fatalf("MediaType = %q, want image/png", res.MediaType)
	}
	if res.ImageName != "pixel.png" {
		t.Fatalf("ImageName = %q, want pixel.png", res.ImageName)
	}
	if _, err := os.Stat(res.ImagePath); err != nil {
		t.Fatalf("ImagePath %q must point at a real, readable file: %v", res.ImagePath, err)
	}
}

func TestWrite_CreatesParentDirs(t *testing.T) {
	dir := testutil.TempDir(t)
	w := NewWriteTool(dir, true, nil)

	res, _ := w.Execute(context.Background(), mustJSON(t, map[string]string{
		"path":    "sub/dir/deep/file.txt",
		"content": "deep",
	}))
	if res.Error != "" {
		t.Fatalf("write error: %s", res.Error)
	}

	if _, err := os.Stat(filepath.Join(dir, "sub/dir/deep/file.txt")); err != nil {
		t.Errorf("expected file to exist: %v", err)
	}
}

func TestWrite_PreservesExistingMode(t *testing.T) {
	dir := testutil.TempDir(t)
	path := filepath.Join(dir, "script.sh")
	if err := os.WriteFile(path, []byte("old"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	w := NewWriteTool(dir, true, nil)
	res, _ := w.Execute(context.Background(), mustJSON(t, map[string]string{"path": "script.sh", "content": "new"}))
	if res.Error != "" {
		t.Fatalf("write error: %s", res.Error)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v, want 0755", info.Mode().Perm())
	}
}

func TestRead_OffsetLimit(t *testing.T) {
	dir := testutil.TempDir(t)
	w := NewWriteTool(dir, true, nil)
	r := NewReadTool(dir, true, nil)

	// Write a 10-line file.
	var content strings.Builder
	for i := 1; i <= 10; i++ {
		content.WriteString("line")
		content.WriteString(string(rune('0' + i - 1)))
		// just use a counter
	}
	// simpler: write numbered lines.
	content.Reset()
	for i := 1; i <= 10; i++ {
		content.WriteString("line")
		itoaTest(i, &content)
		content.WriteString("\n")
	}
	w.Execute(context.Background(), mustJSON(t, map[string]string{"path": "nums.txt", "content": content.String()}))

	// Offset 5, limit 3.
	res, _ := r.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"path":   "nums.txt",
		"offset": 5,
		"limit":  3,
	}))
	if res.Error != "" {
		t.Fatalf("read error: %s", res.Error)
	}
	lines := strings.Split(strings.TrimSpace(res.Content), "\n")
	if len(lines) != 3 {
		t.Errorf("expected 3 lines, got %d: %q", len(lines), res.Content)
	}
	if lines[0] != "5: line5" {
		t.Errorf("first line = %q, want %q (offset preserves the real file line number)", lines[0], "5: line5")
	}
}

// TestRead_OffsetLimitAsStrings verifies offset/limit sent as numeric JSON
// strings (some models stringify integer params) are accepted, not rejected
// with a raw Go unmarshal-type-mismatch error.
func TestRead_OffsetLimitAsStrings(t *testing.T) {
	dir := testutil.TempDir(t)
	w := NewWriteTool(dir, true, nil)
	r := NewReadTool(dir, true, nil)

	var content strings.Builder
	for i := 1; i <= 10; i++ {
		content.WriteString("line")
		itoaTest(i, &content)
		content.WriteString("\n")
	}
	w.Execute(context.Background(), mustJSON(t, map[string]string{"path": "nums.txt", "content": content.String()}))

	res, err := r.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"path":   "nums.txt",
		"offset": "5",
		"limit":  "3",
	}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("read error: %s", res.Error)
	}
	lines := strings.Split(strings.TrimSpace(res.Content), "\n")
	if len(lines) != 3 || lines[0] != "5: line5" {
		t.Errorf("got %q, want 3 lines starting at 5: line5", res.Content)
	}
}

// TestRead_OffsetAsRangeStringParsed verifies a range-shaped offset (a
// common slip: "80, 130" meaning "read lines 80 to 130" jammed into the
// single offset field) is parsed instead of hard-rejected — schema keeps
// offset/limit as separate integers, but this one bad shape is rescued.
func TestRead_OffsetAsRangeStringParsed(t *testing.T) {
	dir := testutil.TempDir(t)
	w := NewWriteTool(dir, true, nil)
	r := NewReadTool(dir, true, nil)
	var content strings.Builder
	for i := 1; i <= 200; i++ {
		content.WriteString("line\n")
	}
	w.Execute(context.Background(), mustJSON(t, map[string]string{"path": "f.txt", "content": content.String()}))

	for _, sep := range []string{"80, 90", "80-90"} {
		res, err := r.Execute(context.Background(), mustJSON(t, map[string]interface{}{
			"path":   "f.txt",
			"offset": sep,
		}))
		if err != nil || res.Error != "" {
			t.Fatalf("offset %q: unexpected error: %v %q", sep, err, res.Error)
		}
		if !strings.HasPrefix(res.Content, "80: line") {
			t.Errorf("offset %q: content should start at line 80, got: %q", sep, res.Content)
		}
		if strings.Contains(res.Content, "91: line") {
			t.Errorf("offset %q: content should stop at line 90 (limit=11), got: %q", sep, res.Content)
		}
	}
}

// TestRead_OffsetGarbageStillRejectedClearly verifies an offset that is
// neither a plain integer nor a two-number range still fails with an
// actionable message, not Go's raw
// "json: cannot unmarshal string into Go struct field ... of type int".
func TestRead_OffsetGarbageStillRejectedClearly(t *testing.T) {
	dir := testutil.TempDir(t)
	w := NewWriteTool(dir, true, nil)
	r := NewReadTool(dir, true, nil)
	w.Execute(context.Background(), mustJSON(t, map[string]string{"path": "f.txt", "content": "hi\n"}))

	res, _ := r.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"path":   "f.txt",
		"offset": "not a number",
	}))
	if res.Error == "" {
		t.Fatal("expected an error for a non-numeric offset")
	}
	if strings.Contains(res.Error, "cannot unmarshal string into Go struct field") {
		t.Errorf("error leaks a raw Go type-mismatch message, want an actionable one: %s", res.Error)
	}
	if !strings.Contains(res.Error, "single integer") {
		t.Errorf("error should explain offset must be a single integer, got: %s", res.Error)
	}
}

func itoaTest(n int, b *strings.Builder) {
	if n == 0 {
		b.WriteByte('0')
		return
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	b.Write(digits)
}

// TestRead_TruncationCountsLinesCorrectly verifies that when the byte cap is
// hit with zero bytes remaining for the next line, the reported line count is
// not inflated by one (off-by-one fix).
func TestRead_TruncationCountsLinesCorrectly(t *testing.T) {
	dir := testutil.TempDir(t)
	path := filepath.Join(dir, "exact.txt")

	// One full line that exactly fills maxBytes (including its trailing \n),
	// followed by a second line. When reading, the second line has 0 remaining
	// bytes, so no partial line is written and the count should stay at 1.
	firstLine := strings.Repeat("a", maxBytes-1) + "\n"
	if err := os.WriteFile(path, []byte(firstLine+"more\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	r := NewReadTool(dir, true, nil)
	res, _ := r.Execute(context.Background(), mustJSON(t, map[string]string{"path": "exact.txt"}))
	if res.Error != "" {
		t.Fatalf("read error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "1 lines shown") {
		t.Errorf("expected '1 lines shown' in truncated output, got: %q", res.Content)
	}
	if strings.Contains(res.Content, "2 lines shown") {
		t.Errorf("off-by-one: reported 2 lines shown, got: %q", res.Content)
	}
}

func TestEdit(t *testing.T) {
	dir := testutil.TempDir(t)
	w := NewWriteTool(dir, true, nil)
	e := NewEditTool(dir, true, nil)

	content := "alpha\nbeta\ngamma\n"
	w.Execute(context.Background(), mustJSON(t, map[string]string{"path": "f.txt", "content": content}))

	// Edit beta → BETA.
	res, _ := e.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"path": "f.txt",
		"edits": []map[string]string{
			{"oldText": "beta", "newText": "BETA"},
		},
	}))
	if res.Error != "" {
		t.Fatalf("edit error: %s", res.Error)
	}

	got, _ := os.ReadFile(filepath.Join(dir, "f.txt"))
	if !strings.Contains(string(got), "BETA") {
		t.Errorf("expected BETA in file, got %q", got)
	}
	if strings.Contains(string(got), "beta") {
		t.Errorf("beta should have been replaced: %q", got)
	}
}

func TestEdit_NonUniqueFails(t *testing.T) {
	dir := testutil.TempDir(t)
	w := NewWriteTool(dir, true, nil)
	e := NewEditTool(dir, true, nil)

	content := "foo\nfoo\n"
	w.Execute(context.Background(), mustJSON(t, map[string]string{"path": "f.txt", "content": content}))

	res, _ := e.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"path": "f.txt",
		"edits": []map[string]string{
			{"oldText": "foo", "newText": "bar"},
		},
	}))
	if res.Error == "" {
		t.Error("expected error for non-unique oldText")
	}
	if !strings.Contains(res.Error, "not unique") {
		t.Errorf("expected 'not unique' in error, got %q", res.Error)
	}
}

func TestEdit_MissingFails(t *testing.T) {
	dir := testutil.TempDir(t)
	w := NewWriteTool(dir, true, nil)
	e := NewEditTool(dir, true, nil)

	w.Execute(context.Background(), mustJSON(t, map[string]string{"path": "f.txt", "content": "foo\n"}))

	res, _ := e.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"path": "f.txt",
		"edits": []map[string]string{
			{"oldText": "nonexistent", "newText": "bar"},
		},
	}))
	if res.Error == "" {
		t.Error("expected error for missing oldText")
	}
	if !strings.Contains(res.Error, "not found") {
		t.Errorf("expected 'not found' in error, got %q", res.Error)
	}
}

// TestEdit_MissingHintsWhitespaceMismatch verifies the diagnostic hint fires
// when oldText exists in the file but with different whitespace — the most
// common real cause of a "not found" failure.
func TestEdit_MissingHintsWhitespaceMismatch(t *testing.T) {
	dir := testutil.TempDir(t)
	w := NewWriteTool(dir, true, nil)
	e := NewEditTool(dir, true, nil)

	w.Execute(context.Background(), mustJSON(t, map[string]string{"path": "f.txt", "content": "func foo() {\n    return 1\n}\n"}))

	res, _ := e.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"path": "f.txt",
		"edits": []map[string]string{
			{"oldText": "func foo() {\n\treturn 1\n}", "newText": "func foo() {\n\treturn 2\n}"},
		},
	}))
	if res.Error == "" {
		t.Fatal("expected error for whitespace-mismatched oldText")
	}
	if !strings.Contains(res.Error, "whitespace-only mismatch at line 1") {
		t.Errorf("error = %q, want a whitespace-mismatch hint at line 1", res.Error)
	}
}

// TestEdit_MissingHintsStaleContent verifies the diagnostic hint points at
// the closest surviving line when oldText's content isn't in the file at
// all (file changed since the model last read it).
func TestEdit_MissingHintsStaleContent(t *testing.T) {
	dir := testutil.TempDir(t)
	w := NewWriteTool(dir, true, nil)
	e := NewEditTool(dir, true, nil)

	w.Execute(context.Background(), mustJSON(t, map[string]string{"path": "f.txt", "content": "alpha\nfindThisDistinctiveLine\ngamma\n"}))

	res, _ := e.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"path": "f.txt",
		"edits": []map[string]string{
			{"oldText": "before\nfindThisDistinctiveLine\nafter", "newText": "x"},
		},
	}))
	if res.Error == "" {
		t.Fatal("expected error for stale oldText")
	}
	if !strings.Contains(res.Error, "closest line found is line 2") {
		t.Errorf("error = %q, want a closest-line hint at line 2", res.Error)
	}
}

func TestEdit_MultipleEditsUseOriginalFile(t *testing.T) {
	dir := testutil.TempDir(t)
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	e := NewEditTool(dir, true, nil)

	res, _ := e.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"path": "f.txt",
		"edits": []map[string]string{
			{"oldText": "alpha", "newText": "ALPHA"},
			{"oldText": "gamma", "newText": "GAMMA"},
		},
	}))
	if res.Error != "" {
		t.Fatalf("edit error: %s", res.Error)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "ALPHA\nbeta\nGAMMA\n" {
		t.Fatalf("content = %q", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v, want 0755", info.Mode().Perm())
	}
}

// TestEdit_FlatSingleEditShorthand is the reported live failure: a model
// calling edit with exactly one change reliably reaches for a flat
// {path, oldText, newText} shape instead of wrapping it in edits: [{...}] —
// confirmed from real tool-call failures logged in production ("no edits
// provided" after a flat-shape call). This shape must just work instead of
// erroring, since the model isn't going to reliably remember to wrap it.
func TestEdit_FlatSingleEditShorthand(t *testing.T) {
	dir := testutil.TempDir(t)
	w := NewWriteTool(dir, true, nil)
	e := NewEditTool(dir, true, nil)
	w.Execute(context.Background(), mustJSON(t, map[string]string{"path": "f.txt", "content": "alpha\nbeta\ngamma\n"}))

	res, _ := e.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"path":    "f.txt",
		"oldText": "beta",
		"newText": "BETA",
	}))
	if res.Error != "" {
		t.Fatalf("edit error: %s", res.Error)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "f.txt"))
	if !strings.Contains(string(got), "BETA") {
		t.Errorf("expected BETA in file, got %q", got)
	}
}

// TestEdit_StringEncodedEditsRecovered covers the other real failure class:
// edits sent as a JSON-encoded string (some models double-encode the array)
// instead of a native array. When the string itself decodes to a valid
// edits array, recover it instead of failing with a raw Go unmarshal error.
func TestEdit_StringEncodedEditsRecovered(t *testing.T) {
	dir := testutil.TempDir(t)
	w := NewWriteTool(dir, true, nil)
	e := NewEditTool(dir, true, nil)
	w.Execute(context.Background(), mustJSON(t, map[string]string{"path": "f.txt", "content": "alpha\nbeta\ngamma\n"}))

	raw := `{"path": "f.txt", "edits": "[{\"oldText\": \"beta\", \"newText\": \"BETA\"}]"}`
	res, _ := e.Execute(context.Background(), json.RawMessage(raw))
	if res.Error != "" {
		t.Fatalf("edit error: %s", res.Error)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "f.txt"))
	if !strings.Contains(string(got), "BETA") {
		t.Errorf("expected BETA in file, got %q", got)
	}
}

// TestEdit_GarbledEditsGetsActionableError is the case that can't be
// recovered (the string itself isn't valid JSON, matching a real historical
// failure) — the error must tell the model how to fix its call, not leak a
// raw Go json.Unmarshal type-mismatch message.
func TestEdit_GarbledEditsGetsActionableError(t *testing.T) {
	dir := testutil.TempDir(t)
	e := NewEditTool(dir, true, nil)

	raw := `{"path": "f.txt", "edits": "[{\"oldText\">broken json"}`
	res, _ := e.Execute(context.Background(), json.RawMessage(raw))
	if res.Error == "" {
		t.Fatal("expected an error for garbled edits")
	}
	if !strings.Contains(res.Error, "edits") || strings.Contains(res.Error, "cannot unmarshal") {
		t.Errorf("expected an actionable edits-shape error, got %q", res.Error)
	}
}

func TestEdit_OverlappingEditsFail(t *testing.T) {
	dir := testutil.TempDir(t)
	w := NewWriteTool(dir, true, nil)
	e := NewEditTool(dir, true, nil)
	w.Execute(context.Background(), mustJSON(t, map[string]string{"path": "f.txt", "content": "abcdef\n"}))

	res, _ := e.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"path": "f.txt",
		"edits": []map[string]string{
			{"oldText": "abc", "newText": "ABC"},
			{"oldText": "bc", "newText": "BC"},
		},
	}))
	if res.Error == "" || !strings.Contains(res.Error, "overlaps") {
		t.Fatalf("expected overlap error, got %q", res.Error)
	}
}

// TestBashTool_TimeoutAsString verifies timeout sent as a numeric JSON string
// is accepted (same FlexInt fix applied to read's offset/limit), and that a
// malformed non-numeric value fails with an actionable message instead of
// Go's raw unmarshal-type-mismatch error.
func TestBashTool_TimeoutAsString(t *testing.T) {
	dir := testutil.TempDir(t)
	b := NewBashTool(dir, true, nil)

	res, err := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command":     "echo hi",
		"description": "print hi",
		"timeout":     "5",
	}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("bash error: %s", res.Error)
	}

	res, _ = b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command":     "echo hi",
		"description": "print hi",
		"timeout":     "5, 10",
	}))
	if res.Error == "" {
		t.Fatal("expected an error for a range-shaped timeout")
	}
	if strings.Contains(res.Error, "cannot unmarshal string into Go struct field") {
		t.Errorf("error leaks a raw Go type-mismatch message: %s", res.Error)
	}
	if !strings.Contains(res.Error, "single integer") {
		t.Errorf("error should explain timeout must be a single integer, got: %s", res.Error)
	}
}

// TestBashTool_NoAllowlist verifies there is no deterministic allowlist: even a
// trivially safe command is gated through approvalFn (denied without approval,
// runs when approved).
func TestBashTool_NoAllowlist(t *testing.T) {
	dir := testutil.TempDir(t)

	denied := NewBashTool(dir, false, nil)
	res, _ := denied.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command":     "echo hello",
		"description": "print hello",
	}))
	if res.Error == "" {
		t.Fatal("expected safe command to be gated (no allowlist), got auto-run")
	}

	b := NewBashTool(dir, false, func(context.Context, string, string, string) (bool, string) { return true, "" })
	res, _ = b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command":     "echo hello",
		"description": "print hello",
	}))
	if res.Error != "" {
		t.Fatalf("bash error: %s", res.Error)
	}
	var out bashOutput
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if strings.TrimSpace(out.Stdout) != "hello" {
		t.Errorf("stdout = %q, want 'hello'", out.Stdout)
	}
	if out.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", out.ExitCode)
	}
}

func TestBashTool_SanitizesOutput(t *testing.T) {
	dir := testutil.TempDir(t)
	b := NewBashTool(dir, true, nil)
	res, _ := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command":     `printf '\033[31mred\033[0m\0done'`,
		"description": "print colored text",
	}))
	if res.Error != "" {
		t.Fatalf("bash error: %s", res.Error)
	}
	var out bashOutput
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Stdout != "reddone" {
		t.Fatalf("stdout = %q, want sanitized text", out.Stdout)
	}
}

// Missing workdir self-heals to the session root instead of handing exec.Cmd
// a dead Dir: a missing Dir with SysProcAttr set fails as an opaque
// "fork/exec bash: no such file or directory", not a chdir error, so
// Execute stats it explicitly first and falls back to the session root.
func TestBashTool_WorkdirErrorInStderr(t *testing.T) {
	dir := testutil.TempDir(t)
	b := NewBashTool(dir, true, nil)
	res, _ := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command":     "pwd",
		"description": "print working dir",
		"workdir":     "missing",
	}))
	if res.Error != "" {
		t.Fatalf("bash error: %s", res.Error)
	}
	var out bashOutput
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.ExitCode != 0 {
		t.Fatalf("output = %+v, want exit 0 (self-heal to session root)", out)
	}
	if !strings.Contains(out.Hint, "does not exist") {
		t.Fatalf("output = %+v, want hint about missing workdir", out)
	}
}

func bashOut(t *testing.T, res ToolResult) bashOutput {
	t.Helper()
	if res.Error != "" {
		t.Fatalf("bash error: %s", res.Error)
	}
	var out bashOutput
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatalf("unmarshal: %v\ncontent=%q", err, res.Content)
	}
	return out
}

// TestBashTool_NoCwdPersistence is the positive confirmation of the
// stateless contract: a `cd` in one call must not affect where the next
// call on the same BashTool instance runs — bash keeps no RAM state across
// calls at all now, unlike the removed sticky mechanism.
func TestBashTool_NoCwdPersistence(t *testing.T) {
	dir := testutil.TempDir(t)
	sub := filepath.Join(dir, "nested")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	b := NewBashTool(dir, true, nil)

	bashOut(t, mustExec(t, b, "cd nested", "enter nested"))

	res, _ := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command": "pwd", "description": "print cwd",
	}))
	out := bashOut(t, res)
	pwd, _ := filepath.EvalSymlinks(strings.TrimSpace(out.Stdout))
	root, _ := filepath.EvalSymlinks(dir)
	if pwd != root {
		t.Fatalf("pwd = %q, want session root %q (cd from the prior call must not persist)", pwd, dir)
	}
}

// TestBashTool_NoEnvPersistence: same contract for export.
func TestBashTool_NoEnvPersistence(t *testing.T) {
	dir := testutil.TempDir(t)
	b := NewBashTool(dir, true, nil)

	bashOut(t, mustExec(t, b, "export POISSON_STATELESS_TEST=hello", "set env"))

	res, _ := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command":     `printf '%s' "${POISSON_STATELESS_TEST:-unset}"`,
		"description": "read env",
	}))
	out := bashOut(t, res)
	if out.Stdout != "unset" {
		t.Fatalf("stdout = %q, want \"unset\" (export from the prior call must not persist)", out.Stdout)
	}
}

// TestBashTool_SeparateInstancesAlwaysIsolated: main agent and each subagent
// build their own BashTool (see BuildRegistry) — with no shared state left
// at all, two instances can never interfere with each other by construction.
func TestBashTool_SeparateInstancesAlwaysIsolated(t *testing.T) {
	dir := testutil.TempDir(t)
	sub := filepath.Join(dir, "only-parent")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	parent := NewBashTool(dir, true, nil)
	child := NewBashTool(dir, true, nil)

	bashOut(t, mustExec(t, parent, "cd only-parent", "parent cd"))

	res, _ := child.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command": "pwd", "description": "child pwd",
	}))
	out := bashOut(t, res)
	pwd, _ := filepath.EvalSymlinks(strings.TrimSpace(out.Stdout))
	root, _ := filepath.EvalSymlinks(dir)
	if pwd != root {
		t.Fatalf("child pwd = %q, want session root %q (not parent's cwd)", pwd, dir)
	}
}

func mustExec(t *testing.T, b *BashTool, cmd, desc string) ToolResult {
	t.Helper()
	res, err := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command": cmd, "description": desc,
	}))
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestBashTool_UnsafeDenied(t *testing.T) {
	dir := testutil.TempDir(t)
	b := NewBashTool(dir, false, nil) // no approvalFn → deny

	res, _ := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command":     "rm -rf /",
		"description": "remove everything",
	}))
	if res.Error == "" {
		t.Error("expected denial for unsafe command")
	}
}

func TestBashTool_UnsafeApproved(t *testing.T) {
	dir := testutil.TempDir(t)
	approved := func(_ context.Context, command, desc, wd string) (bool, string) { return true, "" }
	b := NewBashTool(dir, false, approved)

	res, _ := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command":     "touch approved.txt",
		"description": "create marker file",
		"workdir":     ".",
	}))
	if res.Error != "" {
		t.Fatalf("bash error: %s", res.Error)
	}
	if _, err := os.Stat(filepath.Join(dir, "approved.txt")); err != nil {
		t.Errorf("expected approved.txt to exist: %v", err)
	}
}

// TestBashTool_PromptsForApproval verifies a dangerous command actually
// invokes the approval callback (with the command + description) rather than
// being auto-denied.
func TestBashTool_PromptsForApproval(t *testing.T) {
	dir := testutil.TempDir(t)
	var gotCmd, gotDesc string
	called := false
	approvalFn := func(_ context.Context, command, desc, wd string) (bool, string) {
		called = true
		gotCmd = command
		gotDesc = desc
		return false, "" // deny, but we only care that it was asked
	}
	b := NewBashTool(dir, false, approvalFn)

	res, _ := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command":     "rm -rf build",
		"description": "clean build dir",
	}))
	if !called {
		t.Fatal("approval callback was not invoked for a dangerous command")
	}
	if gotCmd != "rm -rf build" {
		t.Errorf("approval got command %q, want %q", gotCmd, "rm -rf build")
	}
	if gotDesc != "clean build dir" {
		t.Errorf("approval got description %q, want %q", gotDesc, "clean build dir")
	}
	if res.Error == "" {
		t.Error("expected denial result after approval returned false")
	}
}

// TestBashTool_DenialReasonReachesToolResult verifies a human-supplied denial
// reason ends up in the tool result so the model sees why, not just that.
func TestBashTool_DenialReasonReachesToolResult(t *testing.T) {
	dir := testutil.TempDir(t)
	approvalFn := func(_ context.Context, command, desc, wd string) (bool, string) {
		return false, "not right now, finish the other task first"
	}
	b := NewBashTool(dir, false, approvalFn)

	res, _ := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command":     "rm -rf build",
		"description": "clean build dir",
	}))
	if !strings.Contains(res.Error, "rejected by user") {
		t.Errorf("error = %q, want it to say rejected by user", res.Error)
	}
	if !strings.Contains(res.Error, "reason: not right now, finish the other task first") {
		t.Errorf("error = %q, want the human's reason included", res.Error)
	}
}

// TestBashTool_DenialWithoutReason verifies an empty reason doesn't leave a
// dangling "reason:" clause in the tool result.
func TestBashTool_DenialWithoutReason(t *testing.T) {
	dir := testutil.TempDir(t)
	approvalFn := func(_ context.Context, command, desc, wd string) (bool, string) { return false, "" }
	b := NewBashTool(dir, false, approvalFn)

	res, _ := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command":     "rm -rf build",
		"description": "clean build dir",
	}))
	if !strings.Contains(res.Error, "rejected by user") {
		t.Errorf("error = %q, want it to say rejected by user", res.Error)
	}
	if strings.Contains(res.Error, "reason:") {
		t.Errorf("error = %q, must not contain a dangling reason clause when empty", res.Error)
	}
}

// TestBashTool_MissingDescRejected verifies a call without a description is
// rejected before approval/execution, nudging the model to retry with one —
// rather than silently proceeding with a placeholder purpose.
func TestBashTool_MissingDescRejected(t *testing.T) {
	dir := testutil.TempDir(t)
	called := false
	approvalFn := func(_ context.Context, command, desc, wd string) (bool, string) {
		called = true
		return true, ""
	}
	b := NewBashTool(dir, false, approvalFn)

	for _, desc := range []string{"", "   "} {
		res, _ := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
			"command":     "rm -rf foo",
			"description": desc,
		}))
		if called {
			t.Fatal("approvalFn must not be called when description is missing")
		}
		if !strings.Contains(res.Error, "description is required") {
			t.Errorf("desc %q: error = %q, want it to say description is required", desc, res.Error)
		}
	}
}

func TestBashTool_Sandbox(t *testing.T) {
	dir := testutil.TempDir(t)
	b := NewBashTool(dir, true, nil) // sandbox bypasses guard

	res, _ := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command":     "echo safe_in_sandbox",
		"description": "echo in sandbox",
	}))
	if res.Error != "" {
		t.Fatalf("bash error: %s", res.Error)
	}
	var out bashOutput
	json.Unmarshal([]byte(res.Content), &out)
	if strings.TrimSpace(out.Stdout) != "safe_in_sandbox" {
		t.Errorf("stdout = %q, want 'safe_in_sandbox'", out.Stdout)
	}
}

// TestBashTool_SandboxSkipsDedicatedToolHint verifies sandbox mode also
// skips the dedicated-tool nudge (like the approval gate) — a stand-in
// command like `cat` runs and returns real content with no hint attached.
func TestBashTool_SandboxSkipsDedicatedToolHint(t *testing.T) {
	dir := testutil.TempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := NewBashTool(dir, true, nil) // sandbox bypasses the approval gate and hints

	res, _ := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command":     "cat f.txt",
		"description": "cat in sandbox",
	}))
	if res.Error != "" {
		t.Fatalf("bash error: %s, want no error in sandbox mode", res.Error)
	}
	var out bashOutput
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatalf("unmarshal: %v (res=%+v)", err, res)
	}
	if strings.TrimSpace(out.Stdout) != "hello" {
		t.Errorf("stdout = %q, want 'hello' (sandbox must still execute)", out.Stdout)
	}
	if out.Hint != "" {
		t.Errorf("hint = %q, want none in sandbox mode", out.Hint)
	}
}

// TestBashTool_BackgroundProcessReportsSuccess verifies that backgrounding a
// long-lived process (`cmd &`) is reported as success, not exitCode -1. The
// background child inherits the stdout pipe and keeps it open past bash's
// own exit, tripping Cmd.WaitDelay (2s) — Go returns exec.ErrWaitDelay in
// that exact case (successful exit, pipe never closed), which must not be
// treated as a failed command.
func TestBashTool_BackgroundProcessReportsSuccess(t *testing.T) {
	dir := testutil.TempDir(t)
	b := NewBashTool(dir, true, nil) // sandbox: isolate from the approval gate

	start := time.Now()
	res, _ := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command":     "echo started; sleep 5 &",
		"description": "background a long-lived process",
	}))
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("took %s, want ~2s (WaitDelay) not the full 5s background sleep", elapsed)
	}
	if res.Error != "" {
		t.Fatalf("bash error: %s", res.Error)
	}
	var out bashOutput
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0 (WaitDelay on an orphaned background child is not a failure)", out.ExitCode)
	}
	if !strings.Contains(out.Hint, "background process") {
		t.Errorf("hint = %q, want a note about the background process", out.Hint)
	}
	if strings.TrimSpace(out.Stdout) != "started" {
		t.Errorf("stdout = %q, want 'started'", out.Stdout)
	}
}

// TestBashTool_DedicatedToolHinted verifies a command that is plainly just a
// stand-in for read still runs to completion (approval, real output) and
// gets a "prefer read" hint attached to the result instead of being
// refused — and stays silent for legitimate multi-step/non-equivalent uses,
// and for grep/ls/find, which have no dedicated-tool hint of their own
// (see the guard fast path in agent.WrapRiskGatedApproval instead).
func TestBashTool_DedicatedToolHinted(t *testing.T) {
	dir := testutil.TempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		command string
		want    string // substring expected in the hint, or "" if none expected
	}{
		{"cat", "cat f.txt", "read"},
		{"grep_no_hint", "grep hello f.txt", ""},
		{"ls_no_hint", "ls", ""},
		{"find_name_no_hint", "find . -name f.txt", ""},
		{"sed_range", "sed -n '1,1p' f.txt", "read"},
		{"find_delete_no_hint", "find . -name f.txt -delete", ""},
		{"multi_segment_no_hint", "cat f.txt && echo done", ""},
		{"redirect_no_hint", "cat f.txt > out.txt", ""},
		{"cd_then_cat_hint", "cd . && cat f.txt", "read"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			approvalCalls := 0
			b := NewBashTool(dir, false, func(context.Context, string, string, string) (bool, string) {
				approvalCalls++
				return true, ""
			})
			res, _ := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
				"command":     c.command,
				"description": "test",
			}))
			if res.Error != "" {
				t.Fatalf("command %q: unexpected error: %q", c.command, res.Error)
			}
			if approvalCalls != 1 {
				t.Errorf("command %q: approvalFn called %d times, want 1 (must always run)", c.command, approvalCalls)
			}
			var out bashOutput
			if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
				t.Fatalf("command %q: unmarshal: %v (res=%+v)", c.command, err, res)
			}
			if c.want == "" {
				if out.Hint != "" {
					t.Errorf("command %q: unexpected hint: %q", c.command, out.Hint)
				}
				return
			}
			if !strings.Contains(out.Hint, c.want) {
				t.Errorf("command %q: hint = %q, want it to mention %q", c.command, out.Hint, c.want)
			}
		})
	}
}

// TestBashTool_CdWorkdirHint verifies the workdir nudge fires only for a
// leading `cd DIR &&` with no workdir param already set.
func TestBashTool_CdWorkdirHint(t *testing.T) {
	dir := testutil.TempDir(t)
	b := NewBashTool(dir, false, func(context.Context, string, string, string) (bool, string) { return true, "" })

	res, _ := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command":     "cd sub && echo hi",
		"description": "test",
	}))
	var out bashOutput
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.Contains(out.Hint, "workdir") || !strings.Contains(out.Hint, "sub") {
		t.Errorf("hint = %q, want a workdir nudge mentioning 'sub'", out.Hint)
	}

	res2, _ := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command":     "echo hi",
		"description": "test",
		"workdir":     ".",
	}))
	var out2 bashOutput
	if err := json.Unmarshal([]byte(res2.Content), &out2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if strings.Contains(out2.Hint, "workdir") {
		t.Errorf("hint = %q, want no workdir nudge when workdir is already set / no cd prefix", out2.Hint)
	}
}

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := NewRegistry()
	w := NewWriteTool(testutil.TempDir(t), true, nil)
	r.Register(w)

	got, ok := r.Get("write")
	if !ok {
		t.Fatal("expected to find write tool")
	}
	if got.Name() != "write" {
		t.Errorf("got name %q, want write", got.Name())
	}

	if _, ok := r.Get("nonexistent"); ok {
		t.Error("expected not found for nonexistent tool")
	}
}

func TestRegistry_Definitions(t *testing.T) {
	r := NewRegistry()
	r.Register(NewReadTool("", true, nil))
	r.Register(NewWriteTool("", true, nil))

	defs := r.Definitions()
	if len(defs) != 2 {
		t.Errorf("expected 2 definitions, got %d", len(defs))
	}

	names := []string{}
	for _, d := range defs {
		names = append(names, d.Name)
	}
	sort.Strings(names)
	if names[0] != "read" || names[1] != "write" {
		t.Errorf("expected [read write], got %v", names)
	}
}

type staticTool struct {
	name   string
	result ToolResult
}

func (t staticTool) Name() string            { return t.name }
func (t staticTool) Description() string     { return "static test tool" }
func (t staticTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t staticTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	return t.result, nil
}

func TestRegistry_ExecuteTrimsLargeOutput(t *testing.T) {
	old := toolSpillDir
	toolSpillDir = testutil.TempDir(t)
	defer func() { toolSpillDir = old }()

	const total = 200 * 1024 // well over maxToolOutputBytes
	r := NewRegistry()
	r.Register(staticTool{name: "huge", result: ToolResult{Content: strings.Repeat("x", total)}})
	res, err := r.Execute(context.Background(), "huge", nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Inline content is capped near maxToolOutputBytes (prefix + a short note).
	if len(res.Content) > maxToolOutputBytes+300 {
		t.Fatalf("tool output was not trimmed: len=%d", len(res.Content))
	}
	if !strings.Contains(res.Content, "tool output truncated") {
		t.Fatalf("missing truncation marker: %q", res.Content)
	}
	// The note must report the true total size.
	if !strings.Contains(res.Content, fmt.Sprintf("of %d bytes", total)) {
		t.Fatalf("note must report total size %d: %q", total, res.Content)
	}
	// The full output must be spilled to a readable file in toolSpillDir.
	spills, _ := filepath.Glob(filepath.Join(toolSpillDir, "poisson-tool-*.txt"))
	if len(spills) != 1 {
		t.Fatalf("expected 1 spill file, got %d", len(spills))
	}
	if !strings.Contains(res.Content, spills[0]) {
		t.Fatalf("note must reference the spill path %q: %q", spills[0], res.Content)
	}
	full, err := os.ReadFile(spills[0])
	if err != nil {
		t.Fatalf("read spill: %v", err)
	}
	if len(full) != total {
		t.Fatalf("spill file has %d bytes, want full %d", len(full), total)
	}
}

// TestSweepStaleSpillFiles_RemovesOldOnly is the regression guard for the
// fix that bounds spill-file accumulation in toolSpillDir: nothing else
// ever cleans these up (a spill path only exists as text inside an
// already-trimmed tool result, so there's no per-session record to delete
// it by — see spillFileTTL's doc comment), so age is the only signal.
func TestSweepStaleSpillFiles_RemovesOldOnly(t *testing.T) {
	old := toolSpillDir
	toolSpillDir = testutil.TempDir(t)
	defer func() { toolSpillDir = old }()

	stalePath := filepath.Join(toolSpillDir, "poisson-tool-stale.txt")
	freshPath := filepath.Join(toolSpillDir, "poisson-tool-fresh.txt")
	otherPath := filepath.Join(toolSpillDir, "unrelated.txt")
	for _, p := range []string{stalePath, freshPath, otherPath} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	staleTime := time.Now().Add(-spillFileTTL - time.Hour)
	if err := os.Chtimes(stalePath, staleTime, staleTime); err != nil {
		t.Fatal(err)
	}

	spillSweepOnce = sync.Once{}
	sweepStaleSpillFiles()

	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Errorf("stale spill file should be removed, stat err = %v", err)
	}
	if _, err := os.Stat(freshPath); err != nil {
		t.Errorf("fresh spill file should survive: %v", err)
	}
	if _, err := os.Stat(otherPath); err != nil {
		t.Errorf("non-spill file should be untouched: %v", err)
	}
}

func TestRegistry_ExecuteSanitizesControls(t *testing.T) {
	r := NewRegistry()
	r.Register(staticTool{name: "ansi", result: ToolResult{Content: "ok\x1b[31mred\x1b[0m\x00done"}})
	res, err := r.Execute(context.Background(), "ansi", nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Content != "okreddone" {
		t.Fatalf("content = %q, want sanitized text", res.Content)
	}
}

func TestReadTool_TrimsLongLine(t *testing.T) {
	dir := testutil.TempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "huge.txt"), []byte(strings.Repeat("x", maxBytes+100)), 0o644); err != nil {
		t.Fatalf("write huge file: %v", err)
	}
	res, err := NewReadTool(dir, true, nil).Execute(context.Background(), mustJSON(t, map[string]interface{}{"path": "huge.txt"}))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("read error: %s", res.Error)
	}
	if len(res.Content) > maxBytes+100 {
		t.Fatalf("read output too large: len=%d", len(res.Content))
	}
	if !strings.Contains(res.Content, "output truncated at 50KB") {
		t.Fatalf("missing truncation marker")
	}
}

func TestBashTool_BlocksPoissonYolo(t *testing.T) {
	dir := testutil.TempDir(t)
	// sandbox=true would normally skip the approval gate; the yolo block
	// must still fire so an agent can't nest `px --yolo` under itself.
	b := NewBashTool(dir, true, nil)
	for _, cmd := range []string{
		"px --yolo -p 'do stuff'",
		"px -p --yolo hi",
		"./px --yolo",
		"/usr/local/bin/px --yolo -p x",
		"poisson --yolo",
		"sh -c 'px --yolo -p hi'",
		"env px --yolo -p x",
	} {
		res, _ := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
			"command":     cmd,
			"description": "nested yolo",
		}))
		if res.Error == "" || !strings.Contains(res.Error, "--yolo") {
			t.Errorf("command %q: error = %q, want blocked --yolo", cmd, res.Error)
		}
	}
	// Bare px without --yolo is not this check's job (may still need approval).
	if invokesPoissonYolo("px -p hello") {
		t.Error("px -p without --yolo must not match")
	}
	if invokesPoissonYolo("echo --yolo") {
		t.Error("echo --yolo must not match (no px binary)")
	}
}
