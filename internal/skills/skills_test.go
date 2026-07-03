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

func TestParseSkillQuotedValues(t *testing.T) {
	content := "---\ndescription: 'A quoted skill'\n---\nBody here."
	s := parseSkill(content)
	if s.Description != "A quoted skill" {
		t.Errorf("description = %q, want %q", s.Description, "A quoted skill")
	}
}

func TestDiscover(t *testing.T) {
	tmpHome := testutil.TempHome(t)

	// Create a skill.
	skillDir := filepath.Join(tmpHome, ".poisson", "skills", "test-skill")
	os.MkdirAll(skillDir, 0o700)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\ndescription: \"Test skill\"\n---\nDo the thing."), 0o600)

	skills, err := Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].Name != "test-skill" {
		t.Errorf("name = %q, want test-skill", skills[0].Name)
	}
	if skills[0].Description != "Test skill" {
		t.Errorf("description = %q", skills[0].Description)
	}
	if skills[0].Body != "Do the thing." {
		t.Errorf("body = %q", skills[0].Body)
	}
}

func TestDiscoverEmpty(t *testing.T) {
	testutil.TempHome(t)

	skills, err := Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(skills) != 0 {
		t.Errorf("expected 0 skills, got %d", len(skills))
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
