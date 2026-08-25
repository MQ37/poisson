package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/testutil"
)

// initGitRepo creates a bare-minimum git repo in dir with one committed file
// at two revisions (so a test can cite an older ref, not just HEAD). Own
// copy: internal/citetag has an identical helper for its own resolver
// tests, but this package's only remaining git-repo fixture need is this
// one layout test, not worth a shared export for.
func initGitRepo(t *testing.T) (dir string) {
	t.Helper()
	dir = testutil.TempDir(t)
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	path := filepath.Join(dir, "sample.txt")
	os.WriteFile(path, []byte("old1\nold2\n"), 0644)
	run("add", "sample.txt")
	run("commit", "-q", "-m", "first")
	os.WriteFile(path, []byte("new1\nnew2\nnew3\n"), 0644)
	run("add", "sample.txt")
	run("commit", "-q", "-m", "second")
	return dir
}

func TestLayoutRichMarkdownRendersFileWidget(t *testing.T) {
	dir := testutil.TempDir(t)
	path := filepath.Join(dir, "code.go")
	if err := os.WriteFile(path, []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	raw := "here's the entry point:\n<render file=\"" + path + "\" from=\"1\" to=\"3\"/>\nthat's it"
	lines := layoutRichMarkdown(raw, 60, "")
	joined := strings.Join(lines, "\n")
	plain := stripANSI(joined)

	if !strings.Contains(plain, "╭") || !strings.Contains(plain, "╰") {
		t.Fatalf("expected a bordered widget box, got:\n%s", plain)
	}
	if !strings.Contains(plain, "func main") {
		t.Fatalf("expected file content in the widget, got:\n%s", plain)
	}
	if strings.Contains(plain, "<render") {
		t.Fatalf("literal tag leaked into output instead of expanding: %s", plain)
	}
	if !strings.Contains(plain, "that's it") {
		t.Fatalf("trailing prose after the widget got dropped: %s", plain)
	}
}

// TestLayoutRichMarkdownMidSentenceTagStaysLiteral is the exact rule the
// system prompt teaches the model: a tag sharing a line with other text is
// never expanded — it would visually split the sentence around a full-width
// widget. Confirms the fallback is graceful (literal text), not a crash or
// a silently swallowed tag.
func TestLayoutRichMarkdownMidSentenceTagStaysLiteral(t *testing.T) {
	raw := `see <render file="whatever.go"/> for details`
	lines := layoutRichMarkdown(raw, 60, "")
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, `<render file="whatever.go"/>`) {
		t.Fatalf("expected the tag to render as literal text, got:\n%s", plain)
	}
	if strings.Contains(plain, "╭") {
		t.Fatalf("mid-sentence tag must not expand into a widget box, got:\n%s", plain)
	}
}

func TestLayoutRichMarkdownRendersMissingFileAsError(t *testing.T) {
	raw := `<render file="/no/such/file.go"/>`
	lines := layoutRichMarkdown(raw, 60, "")
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "render error") {
		t.Fatalf("expected a render-error box, got:\n%s", plain)
	}
}

func TestLayoutRichMarkdownRendersGitRefWidget(t *testing.T) {
	dir := initGitRepo(t)
	oldwd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldwd)

	raw := `<render file="sample.txt" ref="HEAD~1"/>`
	lines := layoutRichMarkdown(raw, 60, "")
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "old1") || !strings.Contains(plain, "old2") {
		t.Fatalf("expected the older ref's content, got:\n%s", plain)
	}
	if strings.Contains(plain, "new1") {
		t.Fatalf("got HEAD content instead of the cited ref:\n%s", plain)
	}
}
