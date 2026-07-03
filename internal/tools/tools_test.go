package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

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

func TestWriteThenRead(t *testing.T) {
	dir := testutil.TempDir(t)
	w := NewWriteTool(dir)
	r := NewReadTool(dir)

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
	r := NewReadTool(dir)
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
	w := NewWriteTool(dir)

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
	w := NewWriteTool(dir)
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
	w := NewWriteTool(dir)
	r := NewReadTool(dir)

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
	if lines[0] != "line5" {
		t.Errorf("first line = %q, want line5", lines[0])
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

	r := NewReadTool(dir)
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
	w := NewWriteTool(dir)
	e := NewEditTool(dir)

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
	w := NewWriteTool(dir)
	e := NewEditTool(dir)

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
	w := NewWriteTool(dir)
	e := NewEditTool(dir)

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

func TestEdit_MultipleEditsUseOriginalFile(t *testing.T) {
	dir := testutil.TempDir(t)
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	e := NewEditTool(dir)

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

func TestEdit_OverlappingEditsFail(t *testing.T) {
	dir := testutil.TempDir(t)
	w := NewWriteTool(dir)
	e := NewEditTool(dir)
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
	w := NewWriteTool(dir)
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

func TestSearch_MaxResultsIsGlobal(t *testing.T) {
	dir := testutil.TempDir(t)
	w := NewWriteTool(dir)
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
	w := NewWriteTool(dir)
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
	w := NewWriteTool(dir)
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

func TestGlob(t *testing.T) {
	dir := testutil.TempDir(t)
	w := NewWriteTool(dir)
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
	w := NewWriteTool(dir)
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

	b := NewBashTool(dir, false, func(_, _, _ string) bool { return true })
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
	approved := func(command, desc, wd string) bool { return true }
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
	approvalFn := func(command, desc, wd string) bool {
		called = true
		gotCmd = command
		gotDesc = desc
		return false // deny, but we only care that it was asked
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

// TestBashTool_MissingDescFallback verifies a gated call without a description
// still reaches approval with a placeholder purpose (the guard-reason fallback
// was removed together with the deterministic allowlist).
func TestBashTool_MissingDescFallback(t *testing.T) {
	dir := testutil.TempDir(t)
	var gotDesc string
	called := false
	approvalFn := func(command, desc, wd string) bool {
		called = true
		gotDesc = desc
		return false
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

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := NewRegistry()
	w := NewWriteTool(testutil.TempDir(t))
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
	r.Register(NewReadTool(""))
	r.Register(NewWriteTool(""))

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
	r := NewRegistry()
	r.Register(staticTool{name: "huge", result: ToolResult{Content: strings.Repeat("x", maxToolOutputBytes+100)}})
	res, err := r.Execute(context.Background(), "huge", nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Content) > maxToolOutputBytes+100 {
		t.Fatalf("tool output was not trimmed: len=%d", len(res.Content))
	}
	if !strings.Contains(res.Content, "tool output truncated") {
		t.Fatalf("missing truncation marker: %q", res.Content[len(res.Content)-80:])
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
	res, err := NewReadTool(dir).Execute(context.Background(), mustJSON(t, map[string]interface{}{"path": "huge.txt"}))
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
