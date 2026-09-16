// Package skills discovers and loads SKILL.md files: a fixed set baked into
// the binary (internal/skills/builtin/), plus any user skills found under
// ~/.poisson/skills/. A user skill directory whose name matches a builtin
// skill overrides it, letting users customize a built-in without patching
// the binary.
//
// A skill lives at either <root>/<name>/SKILL.md (ungrouped) or
// <root>/<group>/<name>/SKILL.md (grouped) — one optional level of topic
// grouping, purely for filesystem organization and system-prompt display.
// The skill's Name is still just <name>; group has no effect on lookup or
// on the builtin/user override rule.
package skills

import (
	"embed"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed builtin
var builtinFS embed.FS

// Skill represents a discovered skill.
type Skill struct {
	Name         string
	Group        string // topic directory the skill lives under, "" if ungrouped
	Description  string
	ArgumentHint string
	Body         string // frontmatter stripped
}

// groupDescriptions gives each topic group a one-line blurb shown once in
// the system-prompt listing, in place of a description per skill in that
// group. Hand-maintained: add an entry when introducing a new group
// directory under builtin/ or ~/.poisson/skills/.
var groupDescriptions = map[string]string{
	"code":      "generic software-engineering workflow: quality bar, review, TDD, verification, sandboxed builds, planning, issue/PR/skill authoring",
	"apify":     "Apify-specific conventions: coding standards, repo/team ownership, Kanban PR compliance",
	"data":      "CLI clients for internal data/observability platforms",
	"reporting": "recurring Dailybot status/standup workflows",
	"tools":     "generic external-service CLI wrappers",
}

// Discover returns all builtin skills plus any found under
// ~/.poisson/skills/, sorted by name. A user skill overrides a builtin
// skill of the same name, regardless of which group either lives in.
func Discover() ([]Skill, error) {
	m := make(map[string]Skill)
	for _, s := range builtinSkills() {
		m[s.Name] = s
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	skillsDir := filepath.Join(home, ".poisson", "skills")
	if _, statErr := os.Stat(skillsDir); statErr == nil {
		for _, s := range walkSkills(os.DirFS(skillsDir)) {
			m[s.Name] = s
		}
	} else if !os.IsNotExist(statErr) {
		return nil, statErr
	}

	skills := make([]Skill, 0, len(m))
	for _, s := range m {
		skills = append(skills, s)
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	return skills, nil
}

// builtinSkills parses every SKILL.md embedded under builtin/, one optional
// group level deep.
func builtinSkills() []Skill {
	sub, err := fs.Sub(builtinFS, "builtin")
	if err != nil {
		return nil
	}
	return walkSkills(sub)
}

// walkSkills finds every skill under fsys: either <name>/SKILL.md (ungrouped)
// or <group>/<name>/SKILL.md (one level of topic grouping). A directory that
// is neither a skill dir nor a group of skill dirs is silently ignored.
func walkSkills(fsys fs.FS) []Skill {
	top, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil
	}
	var skills []Skill
	for _, entry := range top {
		if !entry.IsDir() {
			continue
		}
		if s, ok := readSkillDir(fsys, entry.Name(), ""); ok {
			skills = append(skills, s)
			continue
		}
		// Not a skill dir itself — try it as a group, one level deeper.
		sub, err := fs.ReadDir(fsys, entry.Name())
		if err != nil {
			continue
		}
		for _, subEntry := range sub {
			if !subEntry.IsDir() {
				continue
			}
			if s, ok := readSkillDir(fsys, path.Join(entry.Name(), subEntry.Name()), entry.Name()); ok {
				skills = append(skills, s)
			}
		}
	}
	return skills
}

// readSkillDir reads dir/SKILL.md if present and returns the parsed Skill,
// with Name set to dir's own leaf name (frontmatter name is ignored here,
// same as before grouping) and Group set as given.
func readSkillDir(fsys fs.FS, dir, group string) (Skill, bool) {
	data, err := fs.ReadFile(fsys, path.Join(dir, "SKILL.md"))
	if err != nil {
		return Skill{}, false
	}
	s := parseSkill(string(data))
	s.Name = path.Base(dir)
	s.Group = group
	return s, true
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
// injection into the system prompt. Ungrouped skills each get their full
// description, as before. Grouped skills are listed under one header per
// group (name + one-line group description) as bare names only \u2014 the
// group description carries the "when to use one of these" signal instead
// of repeating it per skill, to keep the listing cheap as the skill count
// grows.
func FormatSkillsForPrompt(skills []Skill) string {
	if len(skills) == 0 {
		return ""
	}
	var ungrouped []Skill
	groups := make(map[string][]Skill)
	for _, s := range skills {
		if s.Group == "" {
			ungrouped = append(ungrouped, s)
		} else {
			groups[s.Group] = append(groups[s.Group], s)
		}
	}

	var b strings.Builder
	b.WriteString("\n\nAvailable skills:\n")
	for _, s := range ungrouped {
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

	groupNames := make([]string, 0, len(groups))
	for g := range groups {
		groupNames = append(groupNames, g)
	}
	sort.Strings(groupNames)
	for _, g := range groupNames {
		b.WriteString("\n")
		b.WriteString(g)
		b.WriteString("/")
		if desc := groupDescriptions[g]; desc != "" {
			b.WriteString(" \u2014 ")
			b.WriteString(desc)
		}
		b.WriteString("\n  ")
		names := make([]string, len(groups[g]))
		for i, s := range groups[g] {
			names[i] = s.Name
		}
		sort.Strings(names)
		b.WriteString(strings.Join(names, ", "))
		b.WriteString("\n")
	}
	return b.String()
}
