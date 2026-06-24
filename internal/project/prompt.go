// Package project discovers AGENTS.md files and assembles the system prompt.
package project

import (
	"os"
	"path/filepath"
	"strings"
)

// ContextFile represents a discovered AGENTS.md or CLAUDE.md file.
type ContextFile struct {
	Path    string
	Content string
}

// LoadProjectContextFiles walks the directory tree from cwd to /, collecting
// AGENTS.md files. It also checks ~/.poisson/ for a global AGENTS.md.
// Returns files ordered: [global, ...ancestors_root_to_cwd].
func LoadProjectContextFiles(cwd, agentDir string) []ContextFile {
	var result []ContextFile
	seen := make(map[string]bool)

	// 1. Global (~/.poisson/AGENTS.md).
	if global := loadFromDir(agentDir); global != nil && !seen[global.Path] {
		result = append(result, *global)
		seen[global.Path] = true
	}

	// 2. Walk cwd → root (root-first ordering).
	var ancestors []ContextFile
	current := cwd
	for {
		if f := loadFromDir(current); f != nil && !seen[f.Path] {
			ancestors = append([]ContextFile{*f}, ancestors...)
			seen[f.Path] = true
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}

	result = append(result, ancestors...)
	return result
}

// loadFromDir checks a directory for AGENTS.md or CLAUDE.md.
func loadFromDir(dir string) *ContextFile {
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		return &ContextFile{Path: path, Content: string(data)}
	}
	return nil
}

// BuildSystemPrompt assembles the full system prompt with tools, context
// files, skills, date, and cwd.
func BuildSystemPrompt(opts BuildSystemPromptOptions) string {
	var b strings.Builder

	b.WriteString("You are Poisson, a coding assistant operating in a terminal. ")
	b.WriteString("You help users by reading files, executing commands, editing code, and writing new files.\n\n")

	// Tools.
	if len(opts.ToolNames) > 0 {
		b.WriteString("Available tools:\n")
		for _, name := range opts.ToolNames {
			b.WriteString("- ")
			b.WriteString(name)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// Guidelines.
	b.WriteString("Guidelines:\n")
	b.WriteString("- Be concise in your responses\n")
	b.WriteString("- Show file paths clearly when working with files\n")
	b.WriteString("- Read files in full before wide-ranging changes\n\n")

	// Context files (AGENTS.md).
	if len(opts.ContextFiles) > 0 {
		b.WriteString("<project_context>\n\n")
		b.WriteString("Project-specific instructions and guidelines:\n\n")
		for _, cf := range opts.ContextFiles {
			b.WriteString("<project_instructions path=\"")
			b.WriteString(cf.Path)
			b.WriteString("\">\n")
			b.WriteString(cf.Content)
			b.WriteString("\n</project_instructions>\n\n")
		}
		b.WriteString("</project_context>\n")
	}

	// Skills.
	if opts.SkillsText != "" {
		b.WriteString(opts.SkillsText)
	}

	// Date and cwd.
	b.WriteString("\nCurrent working directory: ")
	b.WriteString(opts.Cwd)
	b.WriteString("\n")

	return b.String()
}

// BuildSystemPromptOptions holds the parameters for building the system prompt.
type BuildSystemPromptOptions struct {
	Cwd          string
	ToolNames    []string
	ContextFiles []ContextFile
	SkillsText   string
}
