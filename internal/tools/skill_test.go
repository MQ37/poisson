package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/skills"
)

// TestNewSkillTool_DuplicateNameLastWins: NewSkillTool builds its lookup map
// by iterating the input slice in order and assigning m[sk[i].Name] = &sk[i]
// for each — a later entry with the same Name overwrites an earlier one, so
// the LAST duplicate in the slice wins. Pinned here because it's easy to
// flip while refactoring (e.g. switching to a "first wins" check).
func TestNewSkillTool_DuplicateNameLastWins(t *testing.T) {
	tool := NewSkillTool([]skills.Skill{
		{Name: "dup", Body: "first body"},
		{Name: "dup", Body: "second body"},
	})

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"name":"dup"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Content != "second body" {
		t.Errorf("Content = %q, want %q (last duplicate should win)", res.Content, "second body")
	}
}

// TestSkillExecute_KnownNameReturnsBody: a known skill name returns its Body
// verbatim as Content, with no error.
func TestSkillExecute_KnownNameReturnsBody(t *testing.T) {
	tool := NewSkillTool([]skills.Skill{
		{Name: "greet", Body: "hello from the skill"},
	})

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"name":"greet"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("Error = %q, want none", res.Error)
	}
	if res.Content != "hello from the skill" {
		t.Errorf("Content = %q, want %q", res.Content, "hello from the skill")
	}
}

// TestSkillExecute_UnknownNameListsAvailable: an unknown name returns an
// error containing "not found" and mentioning at least one real available
// skill name. Map iteration order is nondeterministic, so this asserts set
// membership rather than the exact joined string.
func TestSkillExecute_UnknownNameListsAvailable(t *testing.T) {
	tool := NewSkillTool([]skills.Skill{
		{Name: "alpha", Body: "a"},
		{Name: "bravo", Body: "b"},
	})

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"name":"nope"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Error, "not found") {
		t.Errorf("Error = %q, want it to contain %q", res.Error, "not found")
	}
	if !strings.Contains(res.Error, "nope") {
		t.Errorf("Error = %q, want it to mention the requested name %q", res.Error, "nope")
	}
	if !strings.Contains(res.Error, "alpha") && !strings.Contains(res.Error, "bravo") {
		t.Errorf("Error = %q, want it to mention at least one available skill", res.Error)
	}
}

// TestSkillExecute_ArgsAppendedWhenNonEmpty confirms the exact format:
// "\n\nArguments: <args>" appended to Body only when args is non-empty.
func TestSkillExecute_ArgsAppendedWhenNonEmpty(t *testing.T) {
	tool := NewSkillTool([]skills.Skill{{Name: "s", Body: "body text"}})

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"name":"s","args":"foo bar"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	want := "body text\n\nArguments: foo bar"
	if res.Content != want {
		t.Errorf("Content = %q, want %q", res.Content, want)
	}
}

// TestSkillExecute_NoArgsNoSuffix: empty args must NOT append the
// "Arguments:" suffix at all.
func TestSkillExecute_NoArgsNoSuffix(t *testing.T) {
	tool := NewSkillTool([]skills.Skill{{Name: "s", Body: "body text"}})

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"name":"s","args":""}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Content != "body text" {
		t.Errorf("Content = %q, want %q (no Arguments suffix)", res.Content, "body text")
	}
}

// TestSkillExecute_EmptyNameRequired: name omitted/empty must fail with a
// "name is required"-shaped error, not a "not found" lookup miss.
func TestSkillExecute_EmptyNameRequired(t *testing.T) {
	tool := NewSkillTool(nil)

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"name":""}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Error, "name is required") {
		t.Errorf("Error = %q, want it to contain %q", res.Error, "name is required")
	}
}

// TestSkillExecute_MalformedJSONInvalidInput: input that isn't valid JSON
// must fail with an "invalid input"-shaped error.
func TestSkillExecute_MalformedJSONInvalidInput(t *testing.T) {
	tool := NewSkillTool(nil)

	res, err := tool.Execute(context.Background(), json.RawMessage(`{not json`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Error, "invalid input") {
		t.Errorf("Error = %q, want it to contain %q", res.Error, "invalid input")
	}
}
