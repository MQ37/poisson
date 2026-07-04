package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"poisson/internal/testutil"
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

func TestBuildSystemPrompt(t *testing.T) {
	opts := BuildSystemPromptOptions{
		Cwd:       "/home/user/project",
		ToolNames: []string{"bash", "read", "write"},
		ContextFiles: []ContextFile{
			{Path: "/home/user/project/AGENTS.md", Content: "Always use tabs."},
		},
	}
	prompt := BuildSystemPrompt(opts)
	if !strings.Contains(prompt, "You are Poisson") {
		t.Error("missing base prompt")
	}
	if !strings.Contains(prompt, "bash") {
		t.Error("missing tool name")
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

func TestBuildSystemPromptNoContext(t *testing.T) {
	opts := BuildSystemPromptOptions{
		Cwd:       "/test",
		ToolNames: []string{"bash"},
	}
	prompt := BuildSystemPrompt(opts)
	if strings.Contains(prompt, "project_context") {
		t.Error("should not have project_context with no files")
	}
	if !strings.Contains(prompt, "bash") {
		t.Error("missing tool name")
	}
}

func TestBuildSystemPromptWithSkills(t *testing.T) {
	opts := BuildSystemPromptOptions{
		Cwd:        "/test",
		ToolNames:  []string{"bash"},
		SkillsText: "\n\nAvailable skills:\n- review: Review code\n",
	}
	prompt := BuildSystemPrompt(opts)
	if !strings.Contains(prompt, "review") {
		t.Error("missing skill in prompt")
	}
}
