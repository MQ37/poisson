package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"poisson/internal/testutil"
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
	if !strings.Contains(res.Content, "image/png") || !strings.Contains(res.Content, "base64:") {
		t.Fatalf("expected image metadata and base64, got %q", res.Content)
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

// TestRead_OffsetAsRangeStringRejectedClearly verifies a malformed offset
// (a range like "80, 220" instead of a single integer — an easy mistake to
// make calling this tool) fails with an actionable message, not Go's raw
// "json: cannot unmarshal string into Go struct field ... of type int".
func TestRead_OffsetAsRangeStringRejectedClearly(t *testing.T) {
	dir := testutil.TempDir(t)
	w := NewWriteTool(dir, true, nil)
	r := NewReadTool(dir, true, nil)
	w.Execute(context.Background(), mustJSON(t, map[string]string{"path": "f.txt", "content": "hi\n"}))

	res, _ := r.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"path":   "f.txt",
		"offset": "80, 220",
	}))
	if res.Error == "" {
		t.Fatal("expected an error for a range-shaped offset")
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

func TestSearch(t *testing.T) {
	dir := testutil.TempDir(t)
	w := NewWriteTool(dir, true, nil)
	s := NewSearchTool(dir)

	// Create files with known content.
	w.Execute(context.Background(), mustJSON(t, map[string]string{"path": "a.go", "content": "package main\nfunc foo() {}\n// TODO: fix this\n"}))
	w.Execute(context.Background(), mustJSON(t, map[string]string{"path": "b.go", "content": "package main\nvar bar = 1\n// TODO: another\n"}))
	w.Execute(context.Background(), mustJSON(t, map[string]string{"path": "c.txt", "content": "TODO in txt\n"}))

	res, _ := s.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"pattern": "TODO",
		"path":    ".",
		"glob":    "*.go",
	}))
	if res.Error != "" {
		t.Fatalf("search error: %s", res.Error)
	}
	// Should match a.go and b.go but not c.txt.
	if !strings.Contains(res.Content, "a.go") {
		t.Errorf("expected a.go in results: %q", res.Content)
	}
	if !strings.Contains(res.Content, "b.go") {
		t.Errorf("expected b.go in results: %q", res.Content)
	}
	if strings.Contains(res.Content, "c.txt") {
		t.Errorf("c.txt should not match glob *.go: %q", res.Content)
	}
}

// The model drives search like ripgrep's CLI, passing several space-separated
// paths in one call (e.g. "dirA dirB file.go"). The tool must search each of
// them rather than treating the whole string as a single (missing) path.
func TestSearch_MultipleSpaceSeparatedPaths(t *testing.T) {
	dir := testutil.TempDir(t)
	w := NewWriteTool(dir, true, nil)
	s := NewSearchTool(dir)

	w.Execute(context.Background(), mustJSON(t, map[string]string{"path": "one/a.go", "content": "// TODO one\n"}))
	w.Execute(context.Background(), mustJSON(t, map[string]string{"path": "two/b.go", "content": "// TODO two\n"}))
	w.Execute(context.Background(), mustJSON(t, map[string]string{"path": "three/c.go", "content": "// TODO three\n"}))

	res, _ := s.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"pattern": "TODO",
		"path":    "one two", // two dirs, space-separated; 'three' omitted
	}))
	if res.Error != "" {
		t.Fatalf("search error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "a.go") || !strings.Contains(res.Content, "b.go") {
		t.Errorf("expected matches in both space-separated paths: %q", res.Content)
	}
	if strings.Contains(res.Content, "c.go") {
		t.Errorf("path 'three' was not requested and must not match: %q", res.Content)
	}
}

// TestSearch_AfterContextLines is the reported gap: the model reaching for
// bash `grep -n pattern -A5 file` because search had no way to show lines
// following a match. after (like grep -A) must return them, formatted the
// same way grep itself does ("-" separator for context lines).
func TestSearch_AfterContextLines(t *testing.T) {
	dir := testutil.TempDir(t)
	w := NewWriteTool(dir, true, nil)
	s := NewSearchTool(dir)
	w.Execute(context.Background(), mustJSON(t, map[string]string{
		"path":    "a.txt",
		"content": "one\nMATCH\nthree\nfour\nfive\nsix\n",
	}))

	res, _ := s.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"pattern": "MATCH",
		"path":    ".",
		"after":   2,
	}))
	if res.Error != "" {
		t.Fatalf("search error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "a.txt:2: MATCH") {
		t.Errorf("expected the match line, got %q", res.Content)
	}
	if !strings.Contains(res.Content, "a.txt-3- three") || !strings.Contains(res.Content, "a.txt-4- four") {
		t.Errorf("expected 2 lines of after-context (grep -A style), got %q", res.Content)
	}
	if strings.Contains(res.Content, "five") {
		t.Errorf("context should stop after 2 lines, got %q", res.Content)
	}
}

// TestSearch_BeforeContextLines covers the -B side (before) the same way.
func TestSearch_BeforeContextLines(t *testing.T) {
	dir := testutil.TempDir(t)
	w := NewWriteTool(dir, true, nil)
	s := NewSearchTool(dir)
	w.Execute(context.Background(), mustJSON(t, map[string]string{
		"path":    "a.txt",
		"content": "one\ntwo\nMATCH\nfour\n",
	}))

	res, _ := s.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"pattern": "MATCH",
		"path":    ".",
		"before":  1,
	}))
	if res.Error != "" {
		t.Fatalf("search error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "a.txt-2- two") {
		t.Errorf("expected 1 line of before-context (grep -B style), got %q", res.Content)
	}
	if strings.Contains(res.Content, "one") {
		t.Errorf("context should stop 1 line back, got %q", res.Content)
	}
}

// TestSearch_BeforeAfterAsStrings verifies before/after/max_results sent as
// numeric JSON strings (some models stringify integer params) are accepted,
// not rejected with a raw Go unmarshal-type-mismatch error — same FlexInt
// fix applied to the read tool's offset/limit.
func TestSearch_BeforeAfterAsStrings(t *testing.T) {
	dir := testutil.TempDir(t)
	w := NewWriteTool(dir, true, nil)
	s := NewSearchTool(dir)
	w.Execute(context.Background(), mustJSON(t, map[string]string{
		"path":    "a.txt",
		"content": "one\ntwo\nMATCH\nfour\nfive\n",
	}))

	res, err := s.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"pattern":     "MATCH",
		"path":        ".",
		"before":      "1",
		"after":       "1",
		"max_results": "5",
	}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("search error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "a.txt-2- two") || !strings.Contains(res.Content, "a.txt-4- four") {
		t.Errorf("expected 1 line of before- and after-context, got %q", res.Content)
	}
}

// TestSearch_BeforeAsRangeStringRejectedClearly verifies a malformed before/
// after value fails with an actionable message, not Go's raw unmarshal error.
func TestSearch_BeforeAsRangeStringRejectedClearly(t *testing.T) {
	dir := testutil.TempDir(t)
	s := NewSearchTool(dir)
	res, _ := s.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"pattern": "x",
		"before":  "1, 2",
	}))
	if res.Error == "" {
		t.Fatal("expected an error for a range-shaped before value")
	}
	if strings.Contains(res.Error, "cannot unmarshal string into Go struct field") {
		t.Errorf("error leaks a raw Go type-mismatch message: %s", res.Error)
	}
	if !strings.Contains(res.Error, "single integer") {
		t.Errorf("error should explain before must be a single integer, got: %s", res.Error)
	}
}

func TestSearch_MaxResultsIsGlobal(t *testing.T) {
	dir := testutil.TempDir(t)
	w := NewWriteTool(dir, true, nil)
	s := NewSearchTool(dir)
	w.Execute(context.Background(), mustJSON(t, map[string]string{"path": "a.txt", "content": "hit\nhit\nhit\n"}))
	w.Execute(context.Background(), mustJSON(t, map[string]string{"path": "b.txt", "content": "hit\nhit\nhit\n"}))

	res, _ := s.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"pattern":     "hit",
		"path":        ".",
		"max_results": 2,
	}))
	if res.Error != "" {
		t.Fatalf("search error: %s", res.Error)
	}
	lines := strings.Split(strings.TrimSpace(res.Content), "\n")
	if len(lines) != 3 || !strings.Contains(lines[2], "truncated at 2") {
		t.Fatalf("content = %q, want 2 matches plus truncation", res.Content)
	}
}

func TestSearch_InvalidRegexReturnsError(t *testing.T) {
	dir := testutil.TempDir(t)
	s := NewSearchTool(dir)
	res, _ := s.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"pattern": "[",
		"path":    ".",
	}))
	if res.Error == "" {
		t.Fatalf("expected regex error, got content %q", res.Content)
	}
}

func TestSearch_NoMatches(t *testing.T) {
	dir := testutil.TempDir(t)
	w := NewWriteTool(dir, true, nil)
	s := NewSearchTool(dir)

	w.Execute(context.Background(), mustJSON(t, map[string]string{"path": "f.txt", "content": "hello world\n"}))

	res, _ := s.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"pattern": "NONEXISTENT_PATTERN_XYZ",
		"path":    ".",
	}))
	if res.Error != "" {
		t.Fatalf("search error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "no matches") {
		t.Errorf("expected 'no matches' message: %q", res.Content)
	}
}

// TestSearch_ScannerErrorReported verifies that a match line exceeding the
// scanner buffer is reported, instead of silently returning partial/no results.
func TestSearch_ScannerErrorReported(t *testing.T) {
	dir := testutil.TempDir(t)
	longLine := strings.Repeat("x", 2*1024*1024) + "NEEDLE"
	path := filepath.Join(dir, "huge.txt")
	if err := os.WriteFile(path, []byte(longLine+"\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	s := NewSearchTool(dir)
	res, _ := s.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"pattern": "NEEDLE",
		"path":    ".",
	}))

	// The scanner buffer is 1MB; the rg JSON line for a 2MB match exceeds it.
	// The fix surfaces this as either a ToolResult.Error or a content warning.
	if res.Error != "" {
		if !strings.Contains(res.Error, "scanner error") && !strings.Contains(res.Error, "unreadable") {
			t.Errorf("expected scanner-related error, got: %q", res.Error)
		}
		return
	}
	if !strings.Contains(res.Content, "scanner error") {
		t.Errorf("expected scanner error warning in output, got: %q", res.Content)
	}
}

func TestLs(t *testing.T) {
	dir := testutil.TempDir(t)
	w := NewWriteTool(dir, true, nil)
	ls := NewLsTool(dir)

	// Create some files and a dir.
	w.Execute(context.Background(), mustJSON(t, map[string]string{"path": "file1.txt", "content": "abc"}))
	w.Execute(context.Background(), mustJSON(t, map[string]string{"path": "file2.go", "content": "package main"}))
	w.Execute(context.Background(), mustJSON(t, map[string]string{"path": ".hidden", "content": "secret"}))
	os.Mkdir(filepath.Join(dir, "subdir"), 0o755)

	// Without all: hidden excluded.
	res, _ := ls.Execute(context.Background(), mustJSON(t, map[string]interface{}{"path": "."}))
	if res.Error != "" {
		t.Fatalf("ls error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "file1.txt") {
		t.Errorf("expected file1.txt: %q", res.Content)
	}
	if strings.Contains(res.Content, ".hidden") {
		t.Errorf("should exclude hidden: %q", res.Content)
	}

	// With all: hidden included.
	res, _ = ls.Execute(context.Background(), mustJSON(t, map[string]interface{}{"path": ".", "all": true}))
	if !strings.Contains(res.Content, ".hidden") {
		t.Errorf("expected .hidden with all=true: %q", res.Content)
	}
}

// Recursive ls must not leak dotfiles nested in subdirectories, nor descend
// into hidden subdirectories, when all=false.
func TestLsRecursiveSkipsNestedHidden(t *testing.T) {
	dir := testutil.TempDir(t)
	w := NewWriteTool(dir, true, nil)
	ls := NewLsTool(dir)

	w.Execute(context.Background(), mustJSON(t, map[string]string{"path": "foo/keep.txt", "content": "ok"}))
	w.Execute(context.Background(), mustJSON(t, map[string]string{"path": "foo/.env", "content": "SECRET=1"}))
	w.Execute(context.Background(), mustJSON(t, map[string]string{"path": "foo/.hidden/inner.txt", "content": "x"}))

	res, _ := ls.Execute(context.Background(), mustJSON(t, map[string]interface{}{"path": ".", "recursive": true}))
	if res.Error != "" {
		t.Fatalf("ls error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "keep.txt") {
		t.Errorf("expected non-hidden nested file keep.txt: %q", res.Content)
	}
	if strings.Contains(res.Content, ".env") {
		t.Errorf("nested dotfile foo/.env must be skipped when all=false: %q", res.Content)
	}
	if strings.Contains(res.Content, ".hidden") || strings.Contains(res.Content, "inner.txt") {
		t.Errorf("hidden subdir and its contents must be skipped when all=false: %q", res.Content)
	}

	// With all=true, nested hidden entries reappear.
	res, _ = ls.Execute(context.Background(), mustJSON(t, map[string]interface{}{"path": ".", "recursive": true, "all": true}))
	if !strings.Contains(res.Content, ".env") || !strings.Contains(res.Content, "inner.txt") {
		t.Errorf("expected nested hidden entries with all=true: %q", res.Content)
	}
}

func TestGlob(t *testing.T) {
	dir := testutil.TempDir(t)
	w := NewWriteTool(dir, true, nil)
	g := NewGlobTool(dir)

	w.Execute(context.Background(), mustJSON(t, map[string]string{"path": "a.go", "content": "x"}))
	w.Execute(context.Background(), mustJSON(t, map[string]string{"path": "b.go", "content": "x"}))
	w.Execute(context.Background(), mustJSON(t, map[string]string{"path": "c.txt", "content": "x"}))
	w.Execute(context.Background(), mustJSON(t, map[string]string{"path": "sub/d.go", "content": "x"}))

	res, _ := g.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"pattern": "*.go",
	}))
	if res.Error != "" {
		t.Fatalf("glob error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "a.go") || !strings.Contains(res.Content, "b.go") {
		t.Errorf("expected a.go and b.go: %q", res.Content)
	}
	if strings.Contains(res.Content, "c.txt") {
		t.Errorf("c.txt should not match *.go: %q", res.Content)
	}
}

func TestGlob_Doublestar(t *testing.T) {
	dir := testutil.TempDir(t)
	w := NewWriteTool(dir, true, nil)
	g := NewGlobTool(dir)

	w.Execute(context.Background(), mustJSON(t, map[string]string{"path": "a.go", "content": "x"}))
	w.Execute(context.Background(), mustJSON(t, map[string]string{"path": "sub/b.go", "content": "x"}))
	w.Execute(context.Background(), mustJSON(t, map[string]string{"path": "sub/deep/c.go", "content": "x"}))

	res, _ := g.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"pattern": "**/*.go",
	}))
	if res.Error != "" {
		t.Fatalf("glob error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "a.go") {
		t.Errorf("expected a.go: %q", res.Content)
	}
	if !strings.Contains(res.Content, "sub/b.go") {
		t.Errorf("expected sub/b.go: %q", res.Content)
	}
	if !strings.Contains(res.Content, "sub/deep/c.go") {
		t.Errorf("expected sub/deep/c.go: %q", res.Content)
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
	if out.ExitCode != -1 || out.Stderr == "" {
		t.Fatalf("output = %+v, want exit -1 with stderr", out)
	}
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

// TestBashTool_MissingDescFallback verifies a gated call without a description
// still reaches approval with a placeholder purpose (the guard-reason fallback
// was removed together with the deterministic allowlist).
func TestBashTool_MissingDescFallback(t *testing.T) {
	dir := testutil.TempDir(t)
	var gotDesc string
	called := false
	approvalFn := func(_ context.Context, command, desc, wd string) (bool, string) {
		called = true
		gotDesc = desc
		return false, ""
	}
	b := NewBashTool(dir, false, approvalFn)

	res, _ := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command": "rm -rf foo",
	}))
	if !called {
		t.Fatal("approvalFn must be called even without a description")
	}
	if gotDesc != "(no description provided)" {
		t.Errorf("approval got purpose %q, want placeholder", gotDesc)
	}
	if res.Error == "" {
		t.Error("expected denial after approval returned false")
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

// TestBashTool_DedicatedToolHint verifies the advisory hint fires for
// commands that are plain stand-ins for read/search/glob/ls, and stays
// silent for legitimate multi-step or non-equivalent uses.
func TestBashTool_DedicatedToolHint(t *testing.T) {
	dir := testutil.TempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := NewBashTool(dir, false, func(context.Context, string, string, string) (bool, string) { return true, "" })

	cases := []struct {
		name    string
		command string
		want    string // substring expected in hint, or "" for no hint
	}{
		{"cat", "cat f.txt", "read"},
		{"grep", "grep hello f.txt", "search"},
		{"ls", "ls", "ls"},
		{"find_name", "find . -name f.txt", "glob"},
		{"sed_range", "sed -n '1,2p' f.txt", "read"},
		{"find_delete_no_hint", "find . -name f.txt -delete", ""},
		{"multi_segment_no_hint", "cat f.txt && echo done", ""},
		{"redirect_no_hint", "cat f.txt > out.txt", ""},
		{"cd_then_cat_hint", "cd . && cat f.txt", "read"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, _ := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
				"command":     c.command,
				"description": "test",
			}))
			var out bashOutput
			if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
				t.Fatalf("unmarshal: %v (res=%+v)", err, res)
			}
			if c.want == "" {
				if strings.Contains(out.Hint, "prefer the") || strings.Contains(out.Hint, "this reads a file") {
					t.Errorf("command %q: hint = %q, want no dedicated-tool hint", c.command, out.Hint)
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
