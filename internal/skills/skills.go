// Package skills discovers and loads SKILL.md files from ~/.poisson/skills/.
package skills

import (
	"os"
	"path/filepath"
	"strings"
)

// Skill represents a discovered skill.
type Skill struct {
	Name         string
	Description  string
	ArgumentHint string
	Body         string // frontmatter stripped
}

// Discover scans ~/.poisson/skills/*/SKILL.md and returns all skills.
func Discover() ([]Skill, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	skillsDir := filepath.Join(home, ".poisson", "skills")
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var skills []Skill
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillPath := filepath.Join(skillsDir, entry.Name(), "SKILL.md")
		data, err := os.ReadFile(skillPath)
		if err != nil {
			continue // skip dirs without SKILL.md
		}
		s := parseSkill(string(data))
		s.Name = entry.Name()
		skills = append(skills, s)
	}
	return skills, nil
}

// parseSkill parses frontmatter and body from a SKILL.md file.
func parseSkill(content string) Skill {
	s := Skill{}

	// Parse YAML frontmatter (simple key: value pairs between --- markers).
	if strings.HasPrefix(content, "---\n") {
		end := strings.Index(content[4:], "\n---\n")
		if end == -1 {
			end = strings.Index(content[4:], "\n---")
			if end == -1 {
				s.Body = content
				return s
			}
		}
		frontmatter := content[4 : 4+end]
		bodyStart := 4 + end + 4 // skip past "---\n"
		if bodyStart < len(content) {
			s.Body = strings.TrimSpace(content[bodyStart:])
		}

		parseFrontmatter(frontmatter, &s)
	} else {
		s.Body = strings.TrimSpace(content)
	}

	return s
}

// parseFrontmatter reads simple `key: value` lines plus YAML block scalars
// (`>`, `>-`, `|`, `|-`, and their `+` variants), whose value spans the
// following more-indented lines. Folded (`>`) blocks join with spaces; literal
// (`|`) blocks keep newlines.
func parseFrontmatter(frontmatter string, s *Skill) {
	lines := strings.Split(frontmatter, "\n")
	for i := 0; i < len(lines); i++ {
		raw := lines[i]
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		colon := strings.Index(line, ":")
		if colon == -1 {
			continue
		}
		key := strings.TrimSpace(line[:colon])
		val := strings.TrimSpace(line[colon+1:])

		if folded, literal := blockScalar(val); folded || literal {
			keyIndent := leadingSpaces(raw)
			var block []string
			for i+1 < len(lines) {
				next := lines[i+1]
				if strings.TrimSpace(next) != "" && leadingSpaces(next) <= keyIndent {
					break // a sibling key ends the block
				}
				block = append(block, strings.TrimSpace(next))
				i++
			}
			val = joinBlock(block, folded)
		} else {
			val = strings.Trim(val, `"'`)
		}

		switch key {
		case "description":
			s.Description = val
		case "argument-hint":
			s.ArgumentHint = val
		case "name":
			s.Name = val
		}
	}
}

// blockScalar reports whether val is a YAML block-scalar indicator, and if so
// which style (folded for `>`, literal for `|`).
func blockScalar(val string) (folded, literal bool) {
	switch val {
	case ">", ">-", ">+":
		return true, false
	case "|", "|-", "|+":
		return false, true
	}
	return false, false
}

// joinBlock folds collected block lines: spaces for folded, newlines for
// literal. Trailing blank lines are dropped.
func joinBlock(block []string, folded bool) string {
	for len(block) > 0 && block[len(block)-1] == "" {
		block = block[:len(block)-1]
	}
	sep := "\n"
	if folded {
		sep = " "
	}
	return strings.Join(block, sep)
}

// leadingSpaces counts the leading space/tab indentation of a line.
func leadingSpaces(s string) int {
	n := 0
	for _, r := range s {
		if r == ' ' || r == '\t' {
			n++
			continue
		}
		break
	}
	return n
}

// FormatSkillsForPrompt returns a string listing all available skills for
// injection into the system prompt.
func FormatSkillsForPrompt(skills []Skill) string {
	if len(skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nAvailable skills:\n")
	for _, s := range skills {
		b.WriteString("- ")
		b.WriteString(s.Name)
		if s.Description != "" {
			b.WriteString(": ")
			b.WriteString(s.Description)
		}
		if s.ArgumentHint != "" {
			b.WriteString(" (")
			b.WriteString(s.ArgumentHint)
			b.WriteString(")")
		}
		b.WriteString("\n")
	}
	return b.String()
}
