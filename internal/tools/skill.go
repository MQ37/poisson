package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"poisson/internal/skills"
)

// SkillTool implements the skill tool — loads a SKILL.md by name and
// returns its body as context for the agent.
type SkillTool struct {
	skillMap map[string]*skills.Skill
}

// NewSkillTool creates a skill tool from the given discovered skills.
func NewSkillTool(sk []skills.Skill) *SkillTool {
	m := make(map[string]*skills.Skill, len(sk))
	for i := range sk {
		m[sk[i].Name] = &sk[i]
	}
	return &SkillTool{skillMap: m}
}

func (t *SkillTool) Name() string { return "skill" }

func (t *SkillTool) Description() string {
	return "Load and invoke a skill by name. The skill's instructions are returned as context for you to follow. Prefer this over `read`/`bash cat` for any SKILL.md under ~/.poisson/skills/ — it's the canonical invocation path and keeps skill usage consistent."
}

func (t *SkillTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string", "description": "Skill name (directory under ~/.poisson/skills/)"},
			"args": {"type": "string", "description": "Optional arguments to pass to the skill"}
		},
		"required": ["name"]
	}`)
}

func (t *SkillTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	var params struct {
		Name string `json:"name"`
		Args string `json:"args"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return ToolResult{Error: "invalid input: " + err.Error()}, nil
	}
	if params.Name == "" {
		return ToolResult{Error: "name is required"}, nil
	}

	skill, ok := t.skillMap[params.Name]
	if !ok {
		available := make([]string, 0, len(t.skillMap))
		for name := range t.skillMap {
			available = append(available, name)
		}
		return ToolResult{Error: fmt.Sprintf("skill %q not found. Available: %s", params.Name, strings.Join(available, ", "))}, nil
	}

	result := skill.Body
	if params.Args != "" {
		result += "\n\nArguments: " + params.Args
	}
	return ToolResult{Content: result}, nil
}
