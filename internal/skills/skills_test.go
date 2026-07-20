package skills

import (
	"os"
	"path/filepath"
	"testing"

	"poisson/internal/testutil"
)

func TestParseSkillWithFrontmatter(t *testing.T) {
	content := "---\ndescription: \"Review code quality\"\nargument-hint: \"[file or dir]\"\n---\n\n# Code Review\n\nRead the file and check for bugs."
	s := parseSkill(content)
	if s.Description != "Review code quality" {
		t.Errorf("description = %q, want %q", s.Description, "Review code quality")
	}
	if s.ArgumentHint != "[file or dir]" {
		t.Errorf("argument-hint = %q, want %q", s.ArgumentHint, "[file or dir]")
	}
	if s.Body != "# Code Review\n\nRead the file and check for bugs." {
		t.Errorf("body = %q", s.Body)
	}
}

func TestParseSkillNoFrontmatter(t *testing.T) {
	content := "# Just a skill\n\nDo the thing."
	s := parseSkill(content)
	if s.Body != "# Just a skill\n\nDo the thing." {
		t.Errorf("body = %q", s.Body)
	}
	if s.Description != "" {
		t.Errorf("description should be empty, got %q", s.Description)
	}
}

func TestParseSkillFoldedBlockScalar(t *testing.T) {
	content := "---\nname: notion-cli\ndescription: >-\n  Use the Notion CLI to interact with the API,\n  manage workers, and upload files.\n---\n\n# Body"
	s := parseSkill(content)
	want := "Use the Notion CLI to interact with the API, manage workers, and upload files."
	if s.Description != want {
		t.Errorf("description = %q, want %q", s.Description, want)
	}
	if s.Body != "# Body" {
		t.Errorf("body = %q, want %q", s.Body, "# Body")
	}
}

func TestParseSkillLiteralBlockScalar(t *testing.T) {
	content := "---\ndescription: |\n  line one\n  line two\nname: x\n---\nBody"
	s := parseSkill(content)
	if s.Description != "line one\nline two" {
		t.Errorf("description = %q, want %q", s.Description, "line one\nline two")
	}
	if s.Name != "x" {
		t.Errorf("name = %q, want x (sibling key after block)", s.Name)
	}
}

func TestParseSkillQuotedValues(t *testing.T) {
	content := "---\ndescription: 'A quoted skill'\n---\nBody here."
	s := parseSkill(content)
	if s.Description != "A quoted skill" {
		t.Errorf("description = %q, want %q", s.Description, "A quoted skill")
	}
}

func TestDiscover(t *testing.T) {
	tmpHome := testutil.TempHome(t)

	// Create a user skill alongside the builtin set.
	skillDir := filepath.Join(tmpHome, ".poisson", "skills", "test-skill")
	os.MkdirAll(skillDir, 0o700)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\ndescription: \"Test skill\"\n---\nDo the thing."), 0o600)

	skills, err := Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	want := len(builtinSkills()) + 1
	if len(skills) != want {
		t.Fatalf("expected %d skills (builtin + test-skill), got %d", want, len(skills))
	}
	var found *Skill
	for i := range skills {
		if skills[i].Name == "test-skill" {
			found = &skills[i]
		}
	}
	if found == nil {
		t.Fatalf("test-skill not found in %+v", skills)
	}
	if found.Description != "Test skill" {
		t.Errorf("description = %q", found.Description)
	}
	if found.Body != "Do the thing." {
		t.Errorf("body = %q", found.Body)
	}
}

func TestDiscoverEmpty(t *testing.T) {
	testutil.TempHome(t)

	skills, err := Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(skills) != len(builtinSkills()) {
		t.Errorf("expected only builtin skills, got %d", len(skills))
	}
}

func TestDiscoverUserOverridesBuiltin(t *testing.T) {
	tmpHome := testutil.TempHome(t)

	skillDir := filepath.Join(tmpHome, ".poisson", "skills", "code-quality")
	os.MkdirAll(skillDir, 0o700)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\ndescription: \"Custom override\"\n---\nCustom body."), 0o600)

	skills, err := Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(skills) != len(builtinSkills()) {
		t.Errorf("override should replace, not add: got %d skills", len(skills))
	}
	var found *Skill
	for i := range skills {
		if skills[i].Name == "code-quality" {
			found = &skills[i]
		}
	}
	if found == nil {
		t.Fatal("code-quality not found")
	}
	if found.Description != "Custom override" {
		t.Errorf("description = %q, want user override to win", found.Description)
	}
}

func TestBuiltinSkillsPresent(t *testing.T) {
	testutil.TempHome(t)

	want := []string{
		"check-work", "code-quality", "code-review", "council",
		"create-issue", "create-pr", "review-pr", "stacked-diff-review",
	}
	skills, err := Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	names := make(map[string]bool, len(skills))
	for _, s := range skills {
		names[s.Name] = true
		if s.Body == "" {
			t.Errorf("skill %q has empty body", s.Name)
		}
	}
	for _, name := range want {
		if !names[name] {
			t.Errorf("builtin skill %q not discovered", name)
		}
	}
}

func TestFormatSkillsForPrompt(t *testing.T) {
	skills := []Skill{
		{Name: "review", Description: "Review code"},
		{Name: "create-pr", Description: "Create a PR", ArgumentHint: "[title]"},
	}
	result := FormatSkillsForPrompt(skills)
	if !contains(result, "review") {
		t.Errorf("missing 'review' in: %q", result)
	}
	if !contains(result, "Review code") {
		t.Errorf("missing description: %q", result)
	}
	if !contains(result, "create-pr") {
		t.Errorf("missing 'create-pr': %q", result)
	}
	if !contains(result, "[title]") {
		t.Errorf("missing argument-hint: %q", result)
	}
}

func TestFormatSkillsEmpty(t *testing.T) {
	result := FormatSkillsForPrompt(nil)
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && (s[:len(substr)] == substr || contains(s[1:], substr)))
}
