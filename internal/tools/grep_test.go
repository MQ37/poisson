package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/testutil"
)

func TestGrep_FindsMatches(t *testing.T) {
	if _, err := exec.LookPath(rgBin); err != nil {
		t.Skip("rg not on PATH")
	}
	dir := testutil.TempDir(t)
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\nfunc Foo() {}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("Foo in text\n"), 0o644)

	g := NewGrepTool(dir, alwaysApprove)
	res, err := g.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"pattern": "Foo",
		"glob":    "*.go",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != "" {
		t.Fatalf("grep error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "a.go") || !strings.Contains(res.Content, "Foo") {
		t.Fatalf("content = %q", res.Content)
	}
	if strings.Contains(res.Content, "b.txt") {
		t.Fatalf("glob should exclude b.txt: %q", res.Content)
	}
}

func TestGrep_NoMatches(t *testing.T) {
	if _, err := exec.LookPath(rgBin); err != nil {
		t.Skip("rg not on PATH")
	}
	dir := testutil.TempDir(t)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o644)
	g := NewGrepTool(dir, alwaysApprove)
	res, _ := g.Execute(context.Background(), mustJSON(t, map[string]string{"pattern": "zzz_no_such"}))
	if res.Error != "" {
		t.Fatalf("error = %q", res.Error)
	}
	if res.Content != "no matches" {
		t.Fatalf("content = %q", res.Content)
	}
}

func TestGrep_RequiresPattern(t *testing.T) {
	g := NewGrepTool(".", alwaysApprove)
	res, _ := g.Execute(context.Background(), mustJSON(t, map[string]string{}))
	if res.Error == "" || !strings.Contains(res.Error, "pattern") {
		t.Fatalf("error = %q", res.Error)
	}
}

// grep must skip node_modules even when no .gitignore is present — same
// semantics as glob's skipDirNames (tester found a mismatch here).
func TestGrep_SkipsNodeModulesWithoutGitignore(t *testing.T) {
	if _, err := exec.LookPath(rgBin); err != nil {
		t.Skip("rg not on PATH")
	}
	dir := testutil.TempDir(t)
	os.MkdirAll(filepath.Join(dir, "node_modules", "pkg"), 0o755)
	os.WriteFile(filepath.Join(dir, "node_modules", "pkg", "skip.go"), []byte("package pkg\nfunc Hidden() {}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc Visible() {}\n"), 0o644)

	g := NewGrepTool(dir, alwaysApprove)
	res, _ := g.Execute(context.Background(), mustJSON(t, map[string]string{"pattern": "func "}))
	if res.Error != "" {
		t.Fatalf("error: %s", res.Error)
	}
	if strings.Contains(res.Content, "node_modules") || strings.Contains(res.Content, "Hidden") {
		t.Fatalf("should skip node_modules: %q", res.Content)
	}
	if !strings.Contains(res.Content, "Visible") {
		t.Fatalf("should still find main.go: %q", res.Content)
	}
}

// TestGrep_SensitivePathGated is the regression guard for the fix that gave
// grep the same sensitive-path approval gate read/write/edit already had —
// without it, grep(path: "~/.ssh") returned key file contents with no
// human ever asked.
func TestGrep_SensitivePathGated(t *testing.T) {
	if _, err := exec.LookPath(rgBin); err != nil {
		t.Skip("rg not on PATH")
	}
	dir := testutil.TempDir(t)
	sshDir := filepath.Join(dir, ".ssh")
	os.MkdirAll(sshDir, 0o755)
	os.WriteFile(filepath.Join(sshDir, "id_rsa"), []byte("PRIVATE KEY MATERIAL"), 0o600)

	denied := NewGrepTool(dir, nil)
	res, _ := denied.Execute(context.Background(), mustJSON(t, map[string]string{"pattern": "KEY", "path": sshDir}))
	if res.Error == "" {
		t.Fatalf("expected sensitive path to be denied, got content: %q", res.Content)
	}

	approved := NewGrepTool(dir, func(context.Context, string, string, string) (bool, string) { return true, "" })
	res, _ = approved.Execute(context.Background(), mustJSON(t, map[string]string{"pattern": "KEY", "path": sshDir}))
	if res.Error != "" || !strings.Contains(res.Content, "PRIVATE KEY MATERIAL") {
		t.Fatalf("expected approved grep to succeed, got error=%q content=%q", res.Error, res.Content)
	}
}
