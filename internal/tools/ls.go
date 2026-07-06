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

// LsTool lists directory contents with file types and sizes.
type LsTool struct {
	cwd string
}

func NewLsTool(cwd string) *LsTool { return &LsTool{cwd: cwd} }

func (t *LsTool) Name() string { return "ls" }

func (t *LsTool) Description() string {
	return "List directory contents with file types and sizes."
}

func (t *LsTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": { "type": "string", "description": "Directory path (default: cwd)" },
    "all": { "type": "boolean", "description": "Show hidden files" },
    "recursive": { "type": "boolean", "description": "List recursively" }
  }
}`)
}

type lsInput struct {
	Path      string `json:"path"`
	All       bool   `json:"all"`
	Recursive bool   `json:"recursive"`
}

func (t *LsTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	var in lsInput
	if err := json.Unmarshal(input, &in); err != nil {
		return ToolResult{Error: "invalid input: " + err.Error()}, nil
	}

	dir := in.Path
	if dir == "" {
		dir = t.cwd
	}
	dir = resolvePath(t.cwd, dir)

	var entries []string
	truncated := false
	if in.Recursive {
		walkErr := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if e := ctx.Err(); e != nil {
				return e
			}
			if path == dir {
				return nil
			}
			// Skip VCS metadata to avoid walking huge .git trees.
			if info.IsDir() && info.Name() == ".git" {
				return filepath.SkipDir
			}
			rel, _ := filepath.Rel(dir, path)
			// Skip dotfiles at any depth: test the entry's own basename and prune
			// hidden directories so nothing beneath them is walked or listed.
			if !in.All && strings.HasPrefix(info.Name(), ".") {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			entries = append(entries, formatEntry(rel, info))
			if len(entries) >= walkMaxResults {
				truncated = true
				return errWalkLimit
			}
			return nil
		})
		if walkErr != nil && !errors.Is(walkErr, errWalkLimit) {
			return ToolResult{Error: "walk error: " + walkErr.Error()}, nil
		}
	} else {
		infos, err := os.ReadDir(dir)
		if err != nil {
			return ToolResult{Error: "cannot read directory: " + err.Error()}, nil
		}
		var names []string
		for _, info := range infos {
			name := info.Name()
			if !in.All && strings.HasPrefix(name, ".") {
				continue
			}
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			full := filepath.Join(dir, name)
			fi, err := os.Lstat(full)
			if err != nil {
				continue
			}
			entries = append(entries, formatEntry(name, fi))
		}
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("Contents of %s:\n", dir))
	if len(entries) == 0 {
		b.WriteString("(empty)\n")
	} else {
		for _, e := range entries {
			b.WriteString(e)
			b.WriteString("\n")
		}
	}
	if truncated {
		b.WriteString(fmt.Sprintf("... (truncated at %d entries)\n", walkMaxResults))
	}
	return ToolResult{Content: b.String()}, nil
}

func formatEntry(name string, info os.FileInfo) string {
	typ := "f"
	size := "-"
	if info.IsDir() {
		typ = "d"
	} else if info.Mode()&os.ModeSymlink != 0 {
		typ = "l"
	} else if info.Mode()&os.ModeNamedPipe != 0 {
		typ = "p"
	} else if info.Mode()&os.ModeSocket != 0 {
		typ = "s"
	}
	if !info.IsDir() {
		size = fmt.Sprintf("%d", info.Size())
	}
	return fmt.Sprintf("%s  %6s  %s", typ, size, name)
}
