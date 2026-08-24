package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/testutil"
)

func writeLinesFile(t *testing.T, dir string, n int) string {
	t.Helper()
	var b strings.Builder
	for i := 1; i <= n; i++ {
		b.WriteString("line")
		b.WriteString(strconv.Itoa(i))
		b.WriteByte('\n')
	}
	path := filepath.Join(dir, "sample.txt")
	if err := os.WriteFile(path, []byte(b.String()), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSliceLinesDefaultsWholeFileCapped(t *testing.T) {
	body, from, to, err := sliceLines("line1\nline2\nline3\n", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if from != 1 || to != 3 {
		t.Fatalf("from=%d to=%d, want 1,3", from, to)
	}
	if body != "1: line1\n2: line2\n3: line3\n" {
		t.Fatalf("body = %q", body)
	}
}

func TestSliceLinesExplicitRange(t *testing.T) {
	body, from, to, err := sliceLines("a\nb\nc\nd\ne\n", 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	if from != 2 || to != 4 {
		t.Fatalf("from=%d to=%d", from, to)
	}
	if body != "2: b\n3: c\n4: d\n" {
		t.Fatalf("body = %q", body)
	}
}

func TestSliceLinesClampsToEnd(t *testing.T) {
	_, from, to, err := sliceLines("a\nb\nc\n", 2, 999)
	if err != nil {
		t.Fatal(err)
	}
	if from != 2 || to != 3 {
		t.Fatalf("from=%d to=%d, want 2,3 (clamped to file end)", from, to)
	}
}

func TestSliceLinesFromBeyondEndErrors(t *testing.T) {
	if _, _, _, err := sliceLines("a\nb\n", 10, 20); err == nil {
		t.Fatal("expected error for from beyond end of content")
	}
}

func TestSliceLinesCapsAtMaxRenderTagLines(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= maxRenderTagLines+100; i++ {
		b.WriteString(strconv.Itoa(i))
		b.WriteByte('\n')
	}
	_, from, to, err := sliceLines(b.String(), 1, maxRenderTagLines+100)
	if err != nil {
		t.Fatal(err)
	}
	if got := to - from + 1; got != maxRenderTagLines {
		t.Fatalf("range size = %d, want capped at %d", got, maxRenderTagLines)
	}
}

func TestSliceLinesEmptyContent(t *testing.T) {
	body, from, to, err := sliceLines("", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if body != "(empty file)" || from != 0 || to != 0 {
		t.Fatalf("body=%q from=%d to=%d", body, from, to)
	}
}

func TestSliceLinesRejectsBinary(t *testing.T) {
	if _, _, _, err := sliceLines("abc\x00def", 0, 0); err == nil {
		t.Fatal("expected error for binary content")
	}
}

func TestReadFileLineRangeReadsFromDisk(t *testing.T) {
	dir := testutil.TempDir(t)
	path := writeLinesFile(t, dir, 5)
	body, from, to, err := readFileLineRange(path, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	if from != 2 || to != 4 || body != "2: line2\n3: line3\n4: line4\n" {
		t.Fatalf("from=%d to=%d body=%q", from, to, body)
	}
}

func TestReadFileLineRangeMissingFileErrors(t *testing.T) {
	if _, _, _, err := readFileLineRange("/no/such/path.txt", 0, 0); err == nil {
		t.Fatal("expected error for missing file")
	}
}

// TestReadFileLineRangeMissingRelativeFileNamesResolvedPath is a regression
// test for a real support case: a model cited a relative path left over
// from a bash workdir override, resolving against the wrong directory. The
// bare os.Open error only echoes the relative path back, giving no hint
// that it resolved somewhere other than intended — the absolute path must
// be in the message so the mismatch is obvious without re-deriving it.
func TestReadFileLineRangeMissingRelativeFileNamesResolvedPath(t *testing.T) {
	_, _, _, err := readFileLineRange("no/such/relative/path.txt", 0, 0)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	abs, absErr := filepath.Abs("no/such/relative/path.txt")
	if absErr != nil {
		t.Fatal(absErr)
	}
	if !strings.Contains(err.Error(), abs) {
		t.Fatalf("error %q does not name resolved absolute path %q", err.Error(), abs)
	}
}

func TestReadFileLineRangeBlocksSensitivePath(t *testing.T) {
	dir := testutil.TempDir(t)
	path := filepath.Join(dir, "id_rsa")
	if err := os.WriteFile(path, []byte("secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := readFileLineRange(path, 0, 0)
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("expected blocked error, got %v", err)
	}
}

// initGitRepo creates a bare-minimum git repo in dir with one committed file
// at two revisions (so tests can cite an older ref, not just HEAD).
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

func TestReadGitLineRangeReadsHead(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := initGitRepo(t)
	body, from, to, err := readGitLineRangeIn(dir, "HEAD", "sample.txt", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if from != 1 || to != 3 || body != "1: new1\n2: new2\n3: new3\n" {
		t.Fatalf("from=%d to=%d body=%q", from, to, body)
	}
}

func TestReadGitLineRangeReadsOlderRef(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := initGitRepo(t)
	body, _, _, err := readGitLineRangeIn(dir, "HEAD~1", "sample.txt", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if body != "1: old1\n2: old2\n" {
		t.Fatalf("body = %q, want the first commit's content", body)
	}
}

func TestReadGitLineRangeBadRefErrors(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := initGitRepo(t)
	if _, _, _, err := readGitLineRangeIn(dir, "not-a-real-ref", "sample.txt", 0, 0); err == nil {
		t.Fatal("expected error for a nonexistent ref")
	}
}

func TestReadGitLineRangeMissingPathAtRefErrors(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := initGitRepo(t)
	if _, _, _, err := readGitLineRangeIn(dir, "HEAD", "nope.txt", 0, 0); err == nil {
		t.Fatal("expected error for a path that doesn't exist at that ref")
	}
}

// TestReadGitLineRangeRefStartingWithDashIsNotAFlag is a regression test: ref
// is model-controlled text, and a plain argv-only exec.Command does NOT stop
// git itself from parsing a ref starting with "-" as a flag rather than a
// revision — a real reproduced arbitrary-file-write via
// `git show --output=/some/path ...`. --end-of-options must close that off.
func TestReadGitLineRangeRefStartingWithDashIsNotAFlag(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := initGitRepo(t)
	outside := filepath.Join(t.TempDir(), "injected.txt")
	maliciousRef := "--output=" + outside
	if _, _, _, err := readGitLineRangeIn(dir, maliciousRef, "sample.txt", 0, 0); err == nil {
		t.Fatal("expected a hostile ref to fail closed, not succeed")
	}
	if _, statErr := os.Stat(outside); statErr == nil {
		t.Fatal("hostile ref wrote a file outside the repo — argument injection succeeded")
	}
}

// TestReadGitLineRangeWorksOutsideAnyRepo is a regression test for a live
// bug: px's own session cwd is routinely a multi-project index directory one
// level above several independent git repos (this repo's own dev workflow —
// see AGENTS.md), never a git repo itself. readGitLineRange (the production
// entrypoint, unlike readGitLineRangeIn used directly by the tests above)
// used to run `git show` with no explicit directory, i.e. implicitly in
// whatever the process's actual cwd happened to be — failing with "not a
// git repository" for every single citation in that (extremely common)
// setup, regardless of how correct the file path was.
func TestReadGitLineRangeWorksOutsideAnyRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repoDir := initGitRepo(t)
	// A sibling directory that is itself not a repo and not inside one —
	// mirrors a multi-project index dir like /home/mq/workdir.
	outsideDir := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(outsideDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldwd)

	abs := filepath.Join(repoDir, "sample.txt")
	body, _, _, err := readGitLineRange("HEAD~1", abs, 0, 0)
	if err != nil {
		t.Fatalf("expected the citation to resolve via the file's own repo, got error: %v", err)
	}
	if body != "1: old1\n2: old2\n" {
		t.Fatalf("body = %q, want the first commit's content", body)
	}
}

// TestReadGitLineRangeNotInAnyRepoNamesResolvedPath is a regression test for
// the git-citation counterpart of
// TestReadFileLineRangeMissingRelativeFileNamesResolvedPath: a relative path
// left over from a citation made inside a different repo (bash workdir
// override, subagent cwd) resolves against the wrong directory here too, and
// the bare "not inside a git repository" error gave no hint where it looked.
func TestReadGitLineRangeNotInAnyRepoNamesResolvedPath(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	outsideDir := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(outsideDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldwd)

	rel := "src/tools/actors/call_actor.ts"
	_, _, _, err = readGitLineRange("HEAD", rel, 0, 0)
	if err == nil {
		t.Fatal("expected error: rel path is not inside any git repository")
	}
	abs := filepath.Join(outsideDir, rel)
	if !strings.Contains(err.Error(), abs) {
		t.Fatalf("error %q does not name resolved absolute path %q", err.Error(), abs)
	}
}

func TestReadGitLineRangeBlocksSensitivePath(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := initGitRepo(t)
	_, _, _, err := readGitLineRangeIn(dir, "HEAD", "id_rsa", 0, 0)
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("expected blocked error for a sensitive path at a git ref, got %v", err)
	}
}
