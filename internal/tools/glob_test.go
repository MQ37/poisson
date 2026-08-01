package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/testutil"
)

// TestGlob_DoubleStarNoExponentialBlowup pins the fix for a pattern with
// several ** boundaries against a deep all-matching-prefix path — the
// unmemoized matcher took >30s past ~14 segments (classic exponential
// backtracking); the memoized DP version must resolve this near-instantly
// regardless of match outcome (this pattern never matches: "nomatch" is
// the last literal segment against an all-"a" path).
func TestGlob_DoubleStarNoExponentialBlowup(t *testing.T) {
	pattern := strings.Repeat("a/**/", 14) + "nomatch"
	rel := strings.Repeat("a/", 30) + "leaf"

	done := make(chan bool, 1)
	go func() {
		ok, err := pathMatch(pattern, rel)
		if err != nil {
			t.Errorf("pathMatch error: %v", err)
		}
		done <- ok
	}()
	select {
	case ok := <-done:
		if ok {
			t.Fatalf("expected no match")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pathMatch took >2s — exponential blowup regression")
	}
}

func TestGlob_BasenamePattern(t *testing.T) {
	dir := testutil.TempDir(t)
	os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "sub", "b.go"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "c.txt"), []byte("x"), 0o644)

	g := NewGlobTool(dir, alwaysApprove)
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

	g := NewGlobTool(dir, alwaysApprove)
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

	g := NewGlobTool(dir, alwaysApprove)
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
	g := NewGlobTool(dir, alwaysApprove)
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

	denied := NewGlobTool(dir, nil)
	res, _ := denied.Execute(context.Background(), mustJSON(t, map[string]string{"pattern": "*", "path": sshDir}))
	if res.Error == "" {
		t.Fatalf("expected sensitive path to be denied, got content: %q", res.Content)
	}

	approved := NewGlobTool(dir, func(context.Context, string, string, string) (bool, string) { return true, "" })
	res, _ = approved.Execute(context.Background(), mustJSON(t, map[string]string{"pattern": "*", "path": sshDir}))
	if res.Error != "" || !strings.Contains(res.Content, "id_rsa") {
		t.Fatalf("expected approved glob to succeed, got error=%q content=%q", res.Error, res.Content)
	}
}
