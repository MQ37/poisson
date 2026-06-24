package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// GlobTool finds files matching a glob pattern.
type GlobTool struct {
	cwd string
}

func NewGlobTool(cwd string) *GlobTool { return &GlobTool{cwd: cwd} }

func (t *GlobTool) Name() string { return "glob" }

func (t *GlobTool) Description() string {
	return "Find files matching a glob pattern."
}

func (t *GlobTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "pattern": { "type": "string", "description": "Glob pattern (e.g. '**/*.go')" },
    "path": { "type": "string", "description": "Base directory (default: cwd)" }
  },
  "required": ["pattern"]
}`)
}

type globInput struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
}

func (t *GlobTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	var in globInput
	if err := json.Unmarshal(input, &in); err != nil {
		return ToolResult{Error: "invalid input: " + err.Error()}, nil
	}
	if in.Pattern == "" {
		return ToolResult{Error: "pattern is required"}, nil
	}

	base := in.Path
	if base == "" {
		base = t.cwd
	}
	base = resolvePath(t.cwd, base)

	var matches []string

	// Handle doublestar (**/) patterns via recursive walk.
	if strings.Contains(in.Pattern, "**") {
		matches = globDoublestar(base, in.Pattern)
	} else {
		// Use filepath.Glob with the full pattern.
		fullPattern := in.Pattern
		if !filepath.IsAbs(fullPattern) {
			fullPattern = filepath.Join(base, fullPattern)
		}
		got, err := filepath.Glob(fullPattern)
		if err != nil {
			return ToolResult{Error: "glob error: " + err.Error()}, nil
		}
		matches = got
	}

	// Make paths relative to base where possible, then sort.
	relMatches := make([]string, 0, len(matches))
	for _, m := range matches {
		rel, err := filepath.Rel(base, m)
		if err == nil && !strings.HasPrefix(rel, "..") {
			relMatches = append(relMatches, rel)
		} else {
			relMatches = append(relMatches, m)
		}
	}
	sort.Strings(relMatches)

	var b strings.Builder
	if len(relMatches) == 0 {
		b.WriteString("no files matched\n")
	} else {
		for _, m := range relMatches {
			b.WriteString(m)
			b.WriteString("\n")
		}
	}
	return ToolResult{Content: b.String()}, nil
}

// globDoublestar expands a pattern containing ** by walking the base directory.
// It supports patterns like "**/*.go", "src/**/*.go", "**/foo".
func globDoublestar(base, pattern string) []string {
	var results []string

	// Split the pattern on the first "**" to get a prefix and suffix.
	parts := strings.SplitN(pattern, "**", 2)
	prefix := strings.TrimPrefix(strings.TrimSuffix(strings.TrimSpace(parts[0]), "/"), "./")
	suffix := ""
	if len(parts) > 1 {
		suffix = strings.TrimPrefix(parts[1], "/")
	}

	// The search root.
	searchRoot := base
	if prefix != "" {
		searchRoot = filepath.Join(base, prefix)
	}

	// Walk the search root, matching the suffix pattern against each path's
	// relative portion.
	filepath.Walk(searchRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(searchRoot, path)
		if err != nil {
			return nil
		}
		if rel == "." {
			return nil
		}
		// Match suffix against the relative path or the basename.
		if suffix == "" {
			results = append(results, path)
			return nil
		}
		// Try matching the suffix as a glob against the full relative path.
		ok, _ := filepath.Match(suffix, filepath.Base(rel))
		if ok {
			results = append(results, path)
			return nil
		}
		// Also try matching the suffix against the full relative path (for
		// patterns like "src/**/*.go" — but we already stripped the prefix).
		if matched, _ := filepath.Match(suffix, rel); matched {
			results = append(results, path)
		}
		return nil
	})

	return results
}
