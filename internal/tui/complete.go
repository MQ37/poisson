package tui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// completionKind distinguishes slash command completion from @file completion.
type completionKind uint8

const (
	completionNone completionKind = iota
	completionSlash
	completionAtFile
)

// completion is the candidate list shown above the input box.
type completion struct {
	kind   completionKind
	prefix string // the partial token the user typed (with leading / or @)
	cands  []string
	idx    int // -1 = no selection; otherwise 0..len-1
}

// empty reports whether the completion has nothing to show.
func (c *completion) empty() bool { return c == nil || len(c.cands) == 0 }

// selected returns the current selection or "" if none.
func (c *completion) selected() string {
	if c.empty() || c.idx < 0 {
		return ""
	}
	return c.cands[c.idx]
}

// cycle moves the selection forward (dir=+1) or backward (dir=-1).
func (c *completion) cycle(dir int) {
	if c.empty() {
		return
	}
	c.idx += dir
	if c.idx < 0 {
		c.idx = len(c.cands) - 1
	} else if c.idx >= len(c.cands) {
		c.idx = 0
	}
}

// reset clears the selection without losing the candidate list.
func (c *completion) reset() { c.idx = -1 }

// slashCommands is the canonical list exposed to the user. Keep in sync with
// commands.go.
var slashCommands = []string{
	"/quit", "/clear", "/help", "/new", "/resume", "/sessions",
	"/search", "/fork", "/undo", "/compact", "/model", "/effort",
	"/models", "/providers", "/reload", "/cost",
}

// matchSlash returns slash commands that start with the given partial token
// (already including the leading "/" if present).
func matchSlash(partial string) []string {
	if !strings.HasPrefix(partial, "/") {
		return nil
	}
	var out []string
	for _, c := range slashCommands {
		if strings.HasPrefix(c, partial) {
			out = append(out, c)
		}
	}
	return out
}

// matchAtFile returns filesystem paths matching partial. partial includes the
// leading "@" and possibly a directory prefix (e.g. "@/home/mq/w").
// Searches relative to cwd.
func matchAtFile(partial string, cwd string) []string {
	if !strings.HasPrefix(partial, "@") {
		return nil
	}
	body := strings.TrimPrefix(partial, "@")
	dirPart, prefix := filepath.Split(body)
	dir := cwd
	if dirPart != "" {
		if filepath.IsAbs(dirPart) {
			dir = dirPart
		} else {
			dir = filepath.Join(cwd, dirPart)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		// Skip dotfiles unless prefix starts with "."
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(prefix, ".") {
			continue
		}
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		// Build the replacement: @<body-with-prefix-replaced>
		repl := "@" + dirPart + name
		if e.IsDir() {
			repl += "/"
		}
		out = append(out, repl)
	}
	sort.Strings(out)
	if len(out) > 16 {
		out = out[:16]
	}
	return out
}

// commonPrefix returns the longest common prefix of all strings, or "" if
// the slice is empty.
func commonPrefixCands(xs []string) string {
	if len(xs) == 0 {
		return ""
	}
	if len(xs) == 1 {
		return xs[0]
	}
	lcp := xs[0]
	for _, s := range xs[1:] {
		lcp = commonPrefix(lcp, s)
		if lcp == "" {
			return ""
		}
	}
	return lcp
}
