package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/testutil"
)

func TestLoadProjectContextFiles(t *testing.T) {
	tmpDir := testutil.TempDir(t)
	subDir := filepath.Join(tmpDir, "project")
	os.MkdirAll(subDir, 0o700)

	// Create AGENTS.md in the subdirectory.
	os.WriteFile(filepath.Join(subDir, "AGENTS.md"), []byte("# Project Rules\nAlways use tabs."), 0o600)

	// Create AGENTS.md in the parent.
	os.WriteFile(filepath.Join(tmpDir, "AGENTS.md"), []byte("# Root Rules\nBe concise."), 0o600)

	// Create global in agentDir.
	agentDir := filepath.Join(tmpDir, ".poisson")
	os.MkdirAll(agentDir, 0o700)
	os.WriteFile(filepath.Join(agentDir, "AGENTS.md"), []byte("# Global Rules\nNever give up."), 0o600)

	// From the subdir with no files read elsewhere: only global + the cwd's own
	// AGENTS.md. The parent (root) one must NOT be auto-included.
	files := LoadProjectContextFiles(subDir, agentDir, nil)
	if len(files) != 2 {
		t.Fatalf("expected 2 context files (global + cwd), got %d", len(files))
	}
	if !strings.Contains(files[0].Content, "Global Rules") {
		t.Errorf("files[0] should be global, got: %s", files[0].Content)
	}
	if !strings.Contains(files[1].Content, "Project Rules") {
		t.Errorf("files[1] should be cwd project, got: %s", files[1].Content)
	}
	for _, f := range files {
		if strings.Contains(f.Content, "Root Rules") {
			t.Fatalf("parent AGENTS.md must not auto-load from a subdir")
		}
	}

	// Once a file was read from the parent dir, its AGENTS.md is included
	// (global, then root, then cwd — shallow to deep).
	files = LoadProjectContextFiles(subDir, agentDir, []string{tmpDir})
	if len(files) != 3 {
		t.Fatalf("expected 3 context files with parent read, got %d", len(files))
	}
	if !strings.Contains(files[1].Content, "Root Rules") {
		t.Errorf("files[1] should be root, got: %s", files[1].Content)
	}
	if !strings.Contains(files[2].Content, "Project Rules") {
		t.Errorf("files[2] should be cwd project, got: %s", files[2].Content)
	}
}

// TestLoadProjectContextFilesDedupsSymlinkedDirAlias is the regression test
// for a symlinked directory reaching the same physical AGENTS.md via two
// distinct path strings — filepath.Clean (used to build dirSet) normalizes
// "."/".." segments but never resolves a symlink, so the real dir and its
// alias used to both load and inject the identical content twice.
func TestLoadProjectContextFilesDedupsSymlinkedDirAlias(t *testing.T) {
	tmpDir := testutil.TempDir(t)
	real := filepath.Join(tmpDir, "real")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "AGENTS.md"), []byte("# Real Rules\nOnce only."), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(tmpDir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink not supported on this filesystem: %v", err)
	}
	agentDir := filepath.Join(tmpDir, ".poisson")
	os.MkdirAll(agentDir, 0o700)

	files := LoadProjectContextFiles(real, agentDir, []string{link})
	count := 0
	for _, f := range files {
		if strings.Contains(f.Content, "Once only") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("got %d copies of the AGENTS.md content via the real dir + its symlink alias, want 1", count)
	}
}

func TestLoadProjectContextFilesNoFiles(t *testing.T) {
	tmpDir := testutil.TempDir(t)
	files := LoadProjectContextFiles(tmpDir, filepath.Join(tmpDir, ".poisson"), nil)
	if len(files) != 0 {
		t.Errorf("expected 0 files, got %d", len(files))
	}
}

func TestLoadProjectContextFilesClaudeMdFallback(t *testing.T) {
	tmpDir := testutil.TempDir(t)
	os.WriteFile(filepath.Join(tmpDir, "CLAUDE.md"), []byte("# Claude rules\nDo good."), 0o600)

	files := LoadProjectContextFiles(tmpDir, filepath.Join(tmpDir, ".poisson"), nil)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if !strings.Contains(files[0].Content, "Claude rules") {
		t.Errorf("should load CLAUDE.md: %s", files[0].Content)
	}
}

func TestLoadProjectContextFilesCapsFileSize(t *testing.T) {
	tmpDir := testutil.TempDir(t)
	// Build an AGENTS.md larger than MaxContextFileSize.
	body := strings.Repeat("A", MaxContextFileSize+100)
	os.WriteFile(filepath.Join(tmpDir, "AGENTS.md"), []byte(body), 0o600)

	files := LoadProjectContextFiles(tmpDir, filepath.Join(tmpDir, ".poisson"), nil)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}

	if len(files[0].Content) > MaxContextFileSize+100 {
		t.Errorf("content size %d exceeds cap plus truncation notice", len(files[0].Content))
	}
	if !strings.Contains(files[0].Content, strings.Repeat("A", MaxContextFileSize-10)) {
		t.Error("content should contain the leading bytes up to the cap")
	}
	if !strings.Contains(files[0].Content, "(file truncated at") {
		t.Errorf("expected truncation notice, got: %q", files[0].Content)
	}
}

func TestContextDirsForFile(t *testing.T) {
	sep := string(filepath.Separator)
	root := sep + "proj"
	sub := filepath.Join(root, "a", "b")

	// cwd == fileDir: just that dir.
	if got := ContextDirsForFile(root, root); len(got) != 1 || got[0] != root {
		t.Errorf("cwd==fileDir: got %v", got)
	}

	// cwd is an ancestor: whole chain shallow→deep.
	got := ContextDirsForFile(root, sub)
	want := []string{root, filepath.Join(root, "a"), sub}
	if len(got) != len(want) {
		t.Fatalf("chain: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("chain[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	// Different branch (no direct path): only the file dir.
	other := sep + "other" + sep + "place"
	if got := ContextDirsForFile(root, other); len(got) != 1 || got[0] != other {
		t.Errorf("different branch: got %v, want [%s]", got, other)
	}

	// Ancestor of cwd is NOT walked (parent is a different branch relative to cwd).
	parent := sep + "proj"
	if got := ContextDirsForFile(filepath.Join(root, "a"), parent); len(got) != 1 || got[0] != parent {
		t.Errorf("parent-of-cwd: got %v", got)
	}
}

func TestBuildSystemPrompt(t *testing.T) {
	opts := BuildSystemPromptOptions{
		Cwd: "/home/user/project",
		ContextFiles: []ContextFile{
			{Path: "/home/user/project/AGENTS.md", Content: "Always use tabs."},
		},
	}
	prompt := BuildSystemPrompt(opts)
	if !strings.Contains(prompt, "You are Poisson") {
		t.Error("missing base prompt")
	}
	if !strings.Contains(prompt, "Always use tabs.") {
		t.Error("missing context file content")
	}
	if !strings.Contains(prompt, "project_context") {
		t.Error("missing project_context tags")
	}
	if !strings.Contains(prompt, "/home/user/project") {
		t.Error("missing cwd")
	}
}

// TestBuildSystemPromptPrefersDedicatedTools guards the proactive guideline
// nudging the model toward read/grep/glob/edit over bash cat/rg/find/sed —
// added because the reactive per-call hint alone didn't change behavior; the
// model needs to be told upfront, not just after it already reached for bash.
func TestBuildSystemPromptPrefersDedicatedTools(t *testing.T) {
	prompt := BuildSystemPrompt(BuildSystemPromptOptions{Cwd: "/test"})
	if !strings.Contains(prompt, "Prefer dedicated tools over bash") {
		t.Errorf("missing dedicated-tool guideline: %q", prompt)
	}
	if !strings.Contains(prompt, "batch") {
		t.Errorf("missing batch guideline: %q", prompt)
	}
	if !strings.Contains(prompt, "Bash is stateless") {
		t.Errorf("missing stateless-bash guideline: %q", prompt)
	}
	if !strings.Contains(prompt, "session cwd") {
		t.Errorf("missing session-cwd file-tool guideline: %q", prompt)
	}
	if !strings.Contains(prompt, "create_sandbox") || !strings.Contains(prompt, "no approval gate") {
		t.Errorf("missing sandbox-preference guideline: %q", prompt)
	}
}

func TestBuildSystemPromptNoContext(t *testing.T) {
	opts := BuildSystemPromptOptions{Cwd: "/test"}
	prompt := BuildSystemPrompt(opts)
	if strings.Contains(prompt, "project_context") {
		t.Error("should not have project_context with no files")
	}
	if !strings.Contains(prompt, "bash") {
		t.Error("missing bash guideline text")
	}
}

// TestBuildSystemPromptNoToolNameList guards the removed "Available tools:"
// bare-name list: every provider already sends the model the full tool
// name+description+schema via its native tool-calling field, so listing bare
// names again in the prompt text is a pure duplicate, not new information.
func TestBuildSystemPromptNoToolNameList(t *testing.T) {
	prompt := BuildSystemPrompt(BuildSystemPromptOptions{Cwd: "/test"})
	if strings.Contains(prompt, "Available tools:") {
		t.Error("bare tool-name list should be removed, it duplicates the native tool schema")
	}
}

// TestBuildSystemPromptDefaultsToSandbox guards the guideline that build/test/
// feature work should default to bash(sandboxId=...), not plain host bash —
// and that the exception for "quick" work is closed, not left open to
// rationalize a multi-step install/build/test chain onto the host (the
// exact miss observed in two real sessions).
func TestBuildSystemPromptDefaultsToSandbox(t *testing.T) {
	prompt := BuildSystemPrompt(BuildSystemPromptOptions{Cwd: "/test"})
	if !strings.Contains(prompt, "Default to a sandbox for actual work") {
		t.Error("missing sandbox-first-for-work guideline")
	}
	if !strings.Contains(prompt, "no exception for") {
		t.Error("missing closed-loophole wording for builds/installs/tests")
	}
}

// TestBuildSystemPromptStoicMantra guards the persona-level compression
// mantra (10-words-beats-two-paragraphs), stated as identity, not just a
// stylistic tip.
func TestBuildSystemPromptStoicMantra(t *testing.T) {
	prompt := BuildSystemPrompt(BuildSystemPromptOptions{Cwd: "/test"})
	if !strings.Contains(prompt, "if it fits in 10 words, that beats two paragraphs of slop") {
		t.Error("missing stoic compression mantra")
	}
	if !strings.Contains(prompt, "task briefs handed to a subagent") {
		t.Error("mantra must extend to subagent/other-agent communication, not just user-facing chat")
	}
}

// TestBuildSystemPromptCommentBrevity guards the explicit word-budget for
// comments (added after agent-authored comments in this repo drifted into
// multi-paragraph history lessons despite the general compression mantra).
func TestBuildSystemPromptCommentBrevity(t *testing.T) {
	prompt := BuildSystemPrompt(BuildSystemPromptOptions{Cwd: "/test"})
	if !strings.Contains(prompt, "Comments: one sentence stating why") {
		t.Error("missing explicit comment-brevity budget")
	}
}

func TestBuildSystemPromptAlwaysIncludesCavemanStyle(t *testing.T) {
	// No config option gates this — it's always on, checked with the minimal
	// options a caller could pass (no tools, no context, no skills).
	prompt := BuildSystemPrompt(BuildSystemPromptOptions{Cwd: "/test"})
	if !strings.Contains(prompt, "respond terse, like smart caveman") {
		t.Error("missing always-on caveman communication style")
	}
	if !strings.Contains(prompt, "Write full sentences, not fragments, for: security warnings") {
		t.Error("missing safety/boundary exception (security warnings need full grammar, not fragments)")
	}
}

// TestBuildSystemPromptRenderTagGuideline guards the <render> citation
// widget guideline: the model must know the syntax exists (or it never
// uses it, per the feature's own design — see internal/tui/render_tag.go),
// and must know the tag has to be alone on its own line, never mid-sentence
// — a full-width widget splits the sentence around it otherwise.
func TestBuildSystemPromptRenderTagGuideline(t *testing.T) {
	prompt := BuildSystemPrompt(BuildSystemPromptOptions{Cwd: "/test"})
	if !strings.Contains(prompt, "<render file=") {
		t.Error("missing <render> tag syntax example")
	}
	if !strings.Contains(prompt, "ref=") {
		t.Error("missing the git ref attribute for citing a commit/branch")
	}
	if !strings.Contains(prompt, "own line") {
		t.Error("missing the never-mid-sentence rule")
	}
	if !strings.Contains(prompt, "ambiguous") {
		t.Error("missing the multi-repo ambiguous-path warning for ref citations")
	}
	if !strings.Contains(prompt, "from must be <= to") {
		t.Error("missing the reversed-range warning")
	}
}

// TestBuildSystemPromptTitleGuideline guards the set_title nudge's strength
// and placement — a softer, buried-in-Guidelines version of this text was
// tried first and empirically failed to get the model to call set_title
// (confirmed live: identical prompt, only this text changed, before/after).
// It must read as unconditional ("MANDATORY", "No exceptions") and sit
// before the Guidelines list, not inside it, for the same reason
// TestBuildSystemPromptPrefersDedicatedTools's dedicated-tools nudge does.
func TestBuildSystemPromptTitleGuideline(t *testing.T) {
	prompt := BuildSystemPrompt(BuildSystemPromptOptions{Cwd: "/test"})
	if !strings.Contains(prompt, "MANDATORY, before any other tool call: call set_title") {
		t.Error("missing the unconditional set_title directive")
	}
	if !strings.Contains(prompt, "No exceptions") {
		t.Error("set_title directive must be unconditional, not a soft suggestion")
	}
	if !strings.Contains(prompt, "kept as history") {
		t.Error("missing the title-history reassurance")
	}
	if idx, guidelinesIdx := strings.Index(prompt, "MANDATORY"), strings.Index(prompt, "Guidelines:"); idx == -1 || guidelinesIdx == -1 || idx > guidelinesIdx {
		t.Error("set_title directive must appear before the Guidelines list, not buried inside it")
	}
}

func TestBuildSystemPromptWithSkills(t *testing.T) {
	opts := BuildSystemPromptOptions{
		Cwd:        "/test",
		SkillsText: "\n\nAvailable skills:\n- review: Review code\n",
	}
	prompt := BuildSystemPrompt(opts)
	if !strings.Contains(prompt, "review") {
		t.Error("missing skill in prompt")
	}
}
