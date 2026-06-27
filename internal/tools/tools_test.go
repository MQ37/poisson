package tools

import (
	"context"
	"encoding/json"
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

func TestBashTool_SafeCommand(t *testing.T) {
	dir := testutil.TempDir(t)
	b := NewBashTool(dir, false, nil)

	res, _ := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
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

// TestBashTool_DescriptionRequiredForGated verifies PR-24: a gated (unsafe) bash
// call without description is rejected at the tool layer with clear error,
// never reaching approvalFn.
func TestBashTool_DescriptionRequiredForGated(t *testing.T) {
	dir := testutil.TempDir(t)
	called := false
	approvalFn := func(command, desc, wd string) bool {
		called = true
		return true
	}
	b := NewBashTool(dir, false, approvalFn)

	res, _ := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command": "rm -rf foo",
		// no description
	}))
	if called {
		t.Error("approvalFn must not be called when description missing for gated cmd")
	}
	if res.Error != "description is required" {
		t.Errorf("got error %q, want %q", res.Error, "description is required")
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

func TestRegistry_ExecuteParallel(t *testing.T) {
	dir := testutil.TempDir(t)
	r := NewRegistry()
	r.Register(NewWriteTool(dir))
	r.Register(NewReadTool(dir))

	// Write 5 files in parallel, then read them.
	var calls []ToolCall
	for i := 0; i < 5; i++ {
		path := "file" + string(rune('0'+i)) + ".txt"
		input, _ := json.Marshal(map[string]string{"path": path, "content": "content"})
		calls = append(calls, ToolCall{Name: "write", Input: input})
	}

	results, err := r.ExecuteParallel(context.Background(), calls)
	if err != nil {
		t.Fatalf("ExecuteParallel: %v", err)
	}
	if len(results) != 5 {
		t.Fatalf("expected 5 results, got %d", len(results))
	}
	for i, res := range results {
		if res.Error != "" {
			t.Errorf("result %d error: %s", i, res.Error)
		}
	}

	// Verify files exist.
	for i := 0; i < 5; i++ {
		path := filepath.Join(dir, "file"+string(rune('0'+i))+".txt")
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected file %s to exist: %v", path, err)
		}
	}

	// Now read them all in parallel.
	var readCalls []ToolCall
	for i := 0; i < 5; i++ {
		path := "file" + string(rune('0'+i)) + ".txt"
		input, _ := json.Marshal(map[string]string{"path": path})
		readCalls = append(readCalls, ToolCall{Name: "read", Input: input})
	}

	results, err = r.ExecuteParallel(context.Background(), readCalls)
	if err != nil {
		t.Fatalf("ExecuteParallel read: %v", err)
	}
	if len(results) != 5 {
		t.Fatalf("expected 5 read results, got %d", len(results))
	}
	for i, res := range results {
		if res.Error != "" {
			t.Errorf("read result %d error: %s", i, res.Error)
		}
		if !strings.Contains(res.Content, "content") {
			t.Errorf("read result %d = %q, want 'content'", i, res.Content)
		}
	}
}

func TestRegistry_ExecuteParallel_OrderPreserved(t *testing.T) {
	dir := testutil.TempDir(t)
	r := NewRegistry()
	r.Register(NewWriteTool(dir))

	// Write files with different content in parallel; verify results are in
	// the same order as input calls.
	var calls []ToolCall
	contents := []string{"aaa", "bbb", "ccc", "ddd", "eee"}
	for i, c := range contents {
		path := "p" + string(rune('0'+i)) + ".txt"
		input, _ := json.Marshal(map[string]string{"path": path, "content": c})
		calls = append(calls, ToolCall{Name: "write", Input: input})
	}

	results, _ := r.ExecuteParallel(context.Background(), calls)
	if len(results) != len(contents) {
		t.Fatalf("expected %d results, got %d", len(contents), len(results))
	}
	for i, res := range results {
		if res.Error != "" {
			t.Errorf("result %d error: %s", i, res.Error)
		}
		// Verify the right file got the right content (order preserved).
		got, _ := os.ReadFile(filepath.Join(dir, "p"+string(rune('0'+i))+".txt"))
		if string(got) != contents[i] {
			t.Errorf("result %d: file content = %q, want %q", i, got, contents[i])
		}
	}
}

func TestRegistry_ExecuteParallel_Concurrency(t *testing.T) {
	// Verify tools actually run concurrently by counting simultaneous
	// executions.
	dir := testutil.TempDir(t)
	r := NewRegistry()

	// We use bash with a sleep to simulate concurrency.
	b := NewBashTool(dir, true, nil)
	r.Register(b)

	var calls []ToolCall
	for i := 0; i < 5; i++ {
		input, _ := json.Marshal(map[string]interface{}{
			"command":     "sleep 0.1; echo done",
			"description": "sleep briefly for concurrency test",
			"timeout":     10,
		})
		calls = append(calls, ToolCall{Name: "bash", Input: input})
	}

	start := time.Now()
	results, err := r.ExecuteParallel(context.Background(), calls)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("ExecuteParallel: %v", err)
	}
	if len(results) != 5 {
		t.Fatalf("expected 5 results, got %d", len(results))
	}
	for _, res := range results {
		if res.Error != "" {
			t.Errorf("result error: %s", res.Error)
		}
	}
	// 5 sleeps of 0.1s: if serial, ~500ms; if parallel, ~100ms.
	// Allow generous margin (use 400ms as threshold).
	if elapsed > 400*time.Millisecond {
		t.Errorf("parallel execution took %v, expected < 400ms (concurrency not working)", elapsed)
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
