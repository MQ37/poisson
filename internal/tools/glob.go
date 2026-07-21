package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// walkMaxResults caps how many entries glob/ls collect from a recursive walk,
// bounding memory and tool-result size. errWalkLimit stops the walk once hit.
const walkMaxResults = 1000

var errWalkLimit = errors.New("walk result limit reached")

// GlobTool finds files matching a glob pattern.
type GlobTool struct {
	cwd string
}

func NewGlobTool(cwd string) *GlobTool { return &GlobTool{cwd: cwd} }

func (t *GlobTool) Name() string { return "glob" }

func (t *GlobTool) Description() string {
	return "Find files matching a glob pattern. Prefer this over bash `find` when locating files by name is the whole command — skips bash's risk-classification step entirely."
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
	if err := requireDir(base); err != nil {
		return ToolResult{Error: "invalid path: " + err.Error()}, nil
	}

	var matches []string
	truncated := false

	// Handle doublestar (**/) patterns via recursive walk.
	if strings.Contains(in.Pattern, "**") {
		matches, truncated = globDoublestar(ctx, base, in.Pattern)
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
	if truncated {
		b.WriteString(fmt.Sprintf("... (truncated at %d matches)\n", walkMaxResults))
	}
	return ToolResult{Content: b.String()}, nil
}

// globDoublestar expands a pattern containing ** by walking the base directory.
// It supports patterns like "**/*.go", "src/**/*.go", "**/foo".
func globDoublestar(ctx context.Context, base, pattern string) (matches []string, truncated bool) {
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
	// relative portion. Honor cancellation and cap results so a huge tree can't
	// hang the tool or blow up the result.
	err := filepath.Walk(searchRoot, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if e := ctx.Err(); e != nil {
			return e
		}
		// Skip VCS metadata: never the target of a glob and can be huge.
		if info.IsDir() && info.Name() == ".git" && path != searchRoot {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(searchRoot, path)
		if err != nil || rel == "." {
			return nil
		}
		matched := suffix == ""
		if !matched {
			if ok, _ := filepath.Match(suffix, filepath.Base(rel)); ok {
				matched = true
			} else if ok, _ := filepath.Match(suffix, rel); ok {
				matched = true
			}
		}
		if matched {
			matches = append(matches, path)
			if len(matches) >= walkMaxResults {
				truncated = true
				return errWalkLimit
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, errWalkLimit) {
		// ctx cancellation or a walk error: return what we collected.
		truncated = truncated || ctx.Err() != nil
	}
	return matches, truncated
}
