package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// GlobTool finds files by glob pattern under a root directory.
type GlobTool struct {
	cwd        string
	sandbox    bool
	approvalFn ApprovalFn
}

func NewGlobTool(cwd string, sandbox bool, approvalFn ApprovalFn) *GlobTool {
	return &GlobTool{cwd: cwd, sandbox: sandbox, approvalFn: approvalFn}
}

func (t *GlobTool) Name() string { return "glob" }

func (t *GlobTool) Description() string {
	return "Find files by glob pattern (e.g. \"**/*.go\", \"internal/**/*_test.go\"). Returns paths relative to the search root, sorted, capped. Prefer this over bash find/ls for file discovery — skips the approval gate. Ignores .git and node_modules by default."
}

func (t *GlobTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "pattern": { "type": "string", "description": "Glob pattern. ** matches across directories. Examples: \"*.go\", \"**/*_test.go\"." },
    "path": { "type": "string", "description": "Root directory to search (default: cwd)" },
    "headLimit": { "type": "integer", "description": "Max paths to return (default 200, hard cap 2000)" }
  },
  "required": ["pattern"]
}`)
}

type globInput struct {
	Pattern   string  `json:"pattern"`
	Path      string  `json:"path"`
	HeadLimit FlexInt `json:"headLimit"`
}

const (
	globDefaultHead = 200
	globMaxHead     = 2000
)

// skipDirNames are never descended into during glob walks.
var skipDirNames = map[string]bool{
	".git":         true,
	"node_modules": true,
	".hg":          true,
	".svn":         true,
	"vendor":       true, // common Go/PHP; still matchable if pattern targets it via path root
}

func (t *GlobTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	var in globInput
	if err := json.Unmarshal(input, &in); err != nil {
		return ToolResult{Error: "invalid input: " + err.Error()}, nil
	}
	pattern := strings.TrimSpace(in.Pattern)
	if pattern == "" {
		return ToolResult{Error: "pattern is required"}, nil
	}

	head := int(in.HeadLimit)
	if head <= 0 {
		head = globDefaultHead
	}
	if head > globMaxHead {
		head = globMaxHead
	}

	root := t.cwd
	if in.Path != "" {
		root = resolvePath(t.cwd, in.Path)
	}
	if res, ok := checkSensitivePath(ctx, t.cwd, t.sandbox, "glob", root, t.approvalFn); !ok {
		return res, nil
	}
	info, err := os.Stat(root)
	if err != nil {
		return ToolResult{Error: "cannot stat path: " + err.Error()}, nil
	}
	if !info.IsDir() {
		// Single file: match basename against pattern.
		base := filepath.Base(root)
		ok, mErr := pathMatch(pattern, base)
		if mErr != nil {
			return ToolResult{Error: "invalid pattern: " + mErr.Error()}, nil
		}
		if !ok {
			// Also try full path form.
			ok, mErr = pathMatch(pattern, root)
			if mErr != nil {
				return ToolResult{Error: "invalid pattern: " + mErr.Error()}, nil
			}
		}
		if ok {
			return ToolResult{Content: root}, nil
		}
		return ToolResult{Content: "no matches"}, nil
	}

	// filepath.Match doesn't support **. Implement a small walker:
	// - pattern without slash → match basenames anywhere under root
	// - pattern with ** or / → match relative path with ** expansion
	var matches []string
	truncated := false
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// Skip unreadable entries; don't fail the whole walk.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if ctx != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
		}
		name := d.Name()
		if d.IsDir() {
			if skipDirNames[name] && p != root {
				return fs.SkipDir
			}
			return nil
		}
		rel, rErr := filepath.Rel(root, p)
		if rErr != nil {
			rel = p
		}
		rel = filepath.ToSlash(rel)
		ok, mErr := pathMatch(pattern, rel)
		if mErr != nil {
			return mErr
		}
		if !ok {
			// Basename-only patterns ("*.go") match anywhere.
			if !strings.ContainsAny(pattern, "/\\") && !strings.Contains(pattern, "**") {
				ok, mErr = pathMatch(pattern, name)
				if mErr != nil {
					return mErr
				}
			}
		}
		if !ok {
			return nil
		}
		if len(matches) >= head {
			truncated = true
			return fs.SkipAll
		}
		matches = append(matches, rel)
		return nil
	})
	if walkErr != nil && walkErr != fs.SkipAll {
		if ctx != nil && walkErr == ctx.Err() {
			return ToolResult{Error: "glob cancelled"}, nil
		}
		// pathMatch error
		if _, ok := walkErr.(*fs.PathError); !ok && !os.IsNotExist(walkErr) {
			return ToolResult{Error: "glob failed: " + walkErr.Error()}, nil
		}
	}

	sort.Strings(matches)
	if len(matches) == 0 {
		return ToolResult{Content: "no matches"}, nil
	}
	body := strings.Join(matches, "\n")
	if truncated {
		body += fmt.Sprintf("\n\n... (truncated at %d paths — raise headLimit, max %d, or narrow pattern/path)", head, globMaxHead)
	} else {
		body += fmt.Sprintf("\n\n(%d paths)", len(matches))
	}
	return ToolResult{Content: body}, nil
}

// pathMatch matches a slash-separated relative path against a glob that may
// contain ** (any number of directories including zero).
func pathMatch(pattern, rel string) (bool, error) {
	pattern = filepath.ToSlash(pattern)
	rel = filepath.ToSlash(rel)
	// Strip leading ./ from both.
	pattern = strings.TrimPrefix(pattern, "./")
	rel = strings.TrimPrefix(rel, "./")

	if !strings.Contains(pattern, "**") {
		ok, err := filepath.Match(pattern, rel)
		if err != nil {
			return false, fmt.Errorf("%w", err)
		}
		// Also try matching with OS separator via filepath on basenames already handled by caller.
		return ok, nil
	}

	// Split on ** and match greedily.
	parts := strings.Split(pattern, "**")
	// Normalize empty edges from leading/trailing **.
	return matchStarStar(parts, rel), nil
}

func matchStarStar(parts []string, rel string) bool {
	// parts joined by **; each part is a filepath.Match pattern possibly
	// with leading/trailing slashes.
	if len(parts) == 0 {
		return rel == ""
	}
	// Leading part must be a prefix match.
	first := strings.TrimPrefix(parts[0], "/")
	first = strings.TrimSuffix(first, "/")
	restParts := parts[1:]

	if first != "" {
		// first may be "src/" style prefix with globs.
		// Find shortest prefix of rel that matches first + optional slash boundary.
		if !consumePrefix(first, &rel) {
			return false
		}
	}
	if len(restParts) == 0 {
		// Pattern ended with ** → remainder can be anything.
		return true
	}
	// Middle / trailing parts: ** can consume any number of leading path
	// segments before each remaining part must match as a suffix or mid segment.
	return matchStarStarTail(restParts, rel)
}

func matchStarStarTail(parts []string, rel string) bool {
	if len(parts) == 0 {
		return true
	}
	last := len(parts) == 1
	part := strings.TrimPrefix(parts[0], "/")
	part = strings.TrimSuffix(part, "/")

	if part == "" {
		// Consecutive ** or trailing ** after split.
		return matchStarStarTail(parts[1:], rel)
	}

	// Try every split point of rel; ** consumes rel[:i], part matches a prefix of rel[i:].
	// For the final part, require the remainder to match exactly (suffix).
	segs := strings.Split(rel, "/")
	for i := 0; i <= len(segs); i++ {
		rest := strings.Join(segs[i:], "/")
		if last {
			ok, err := filepath.Match(part, rest)
			if err == nil && ok {
				return true
			}
			continue
		}
		// Non-final: part must match a prefix path of rest, then recurse.
		if consumePrefix(part, &rest) {
			if matchStarStarTail(parts[1:], rest) {
				return true
			}
		}
	}
	return false
}

// consumePrefix requires that some prefix of *rel matches pattern (slash
// patterns). On success advances *rel past the matched prefix.
func consumePrefix(pattern string, rel *string) bool {
	if pattern == "" {
		return true
	}
	// Exact full match.
	if ok, _ := filepath.Match(pattern, *rel); ok {
		*rel = ""
		return true
	}
	// Try progressive path prefixes.
	segs := strings.Split(*rel, "/")
	for n := 1; n <= len(segs); n++ {
		prefix := strings.Join(segs[:n], "/")
		if ok, _ := filepath.Match(pattern, prefix); ok {
			*rel = strings.Join(segs[n:], "/")
			return true
		}
	}
	return false
}
