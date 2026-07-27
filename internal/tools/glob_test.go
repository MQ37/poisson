package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/testutil"
)

func TestGlob_BasenamePattern(t *testing.T) {
	dir := testutil.TempDir(t)
	os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "sub", "b.go"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "c.txt"), []byte("x"), 0o644)

	g := NewGlobTool(dir, true, nil)
	res, _ := g.Execute(context.Background(), mustJSON(t, map[string]string{"pattern": "*.go"}))
	if res.Error != "" {
		t.Fatalf("error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "a.go") || !strings.Contains(res.Content, "sub/b.go") {
		t.Fatalf("content = %q", res.Content)
	}
	if strings.Contains(res.Content, "c.txt") {
		t.Fatalf("should not match txt: %q", res.Content)
	}
}

func TestGlob_DoubleStar(t *testing.T) {
	dir := testutil.TempDir(t)
	os.MkdirAll(filepath.Join(dir, "internal", "tools"), 0o755)
	os.WriteFile(filepath.Join(dir, "internal", "tools", "edit_test.go"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("x"), 0o644)

	g := NewGlobTool(dir, true, nil)
	res, _ := g.Execute(context.Background(), mustJSON(t, map[string]string{"pattern": "**/*_test.go"}))
	if res.Error != "" {
		t.Fatalf("error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "internal/tools/edit_test.go") {
		t.Fatalf("content = %q", res.Content)
	}
	if strings.Contains(res.Content, "main.go\n") || strings.HasSuffix(strings.TrimSpace(res.Content), "main.go") {
		// count line only if it's a path line
		for _, line := range strings.Split(res.Content, "\n") {
			if line == "main.go" {
				t.Fatalf("main.go should not match: %q", res.Content)
			}
		}
	}
}

func TestGlob_SkipsGit(t *testing.T) {
	dir := testutil.TempDir(t)
	os.MkdirAll(filepath.Join(dir, ".git", "objects"), 0o755)
	os.WriteFile(filepath.Join(dir, ".git", "objects", "x.go"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "ok.go"), []byte("x"), 0o644)

	g := NewGlobTool(dir, true, nil)
	res, _ := g.Execute(context.Background(), mustJSON(t, map[string]string{"pattern": "*.go"}))
	if strings.Contains(res.Content, ".git") {
		t.Fatalf("should skip .git: %q", res.Content)
	}
	if !strings.Contains(res.Content, "ok.go") {
		t.Fatalf("content = %q", res.Content)
	}
}

func TestGlob_NoMatches(t *testing.T) {
	dir := testutil.TempDir(t)
	g := NewGlobTool(dir, true, nil)
	res, _ := g.Execute(context.Background(), mustJSON(t, map[string]string{"pattern": "*.zzz"}))
	if res.Content != "no matches" {
		t.Fatalf("content = %q", res.Content)
	}
}

// TestGlob_SensitivePathGated mirrors TestGrep_SensitivePathGated: glob must
// route through the same sensitive-path approval gate as read/write/edit.
func TestGlob_SensitivePathGated(t *testing.T) {
	dir := testutil.TempDir(t)
	sshDir := filepath.Join(dir, ".ssh")
	os.MkdirAll(sshDir, 0o755)
	os.WriteFile(filepath.Join(sshDir, "id_rsa"), []byte("x"), 0o600)

	denied := NewGlobTool(dir, false, nil)
	res, _ := denied.Execute(context.Background(), mustJSON(t, map[string]string{"pattern": "*", "path": sshDir}))
	if res.Error == "" {
		t.Fatalf("expected sensitive path to be denied, got content: %q", res.Content)
	}

	approved := NewGlobTool(dir, false, func(context.Context, string, string, string) (bool, string) { return true, "" })
	res, _ = approved.Execute(context.Background(), mustJSON(t, map[string]string{"pattern": "*", "path": sshDir}))
	if res.Error != "" || !strings.Contains(res.Content, "id_rsa") {
		t.Fatalf("expected approved glob to succeed, got error=%q content=%q", res.Error, res.Content)
	}
}
