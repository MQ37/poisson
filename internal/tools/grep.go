package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// GrepTool searches file contents via ripgrep.
type GrepTool struct {
	cwd string
}

func NewGrepTool(cwd string) *GrepTool {
	return &GrepTool{cwd: cwd}
}

func (t *GrepTool) Name() string { return "grep" }

func (t *GrepTool) Description() string {
	return "Search file contents with ripgrep. Returns path:line:text matches (content mode). Prefer this over bash rg/grep — line numbers always on, default head_limit caps noise, skips bash's approval gate. Skips .git/node_modules/vendor (same as glob) even without a .gitignore. Large results report truncation; raise headLimit if you need more."
}

func (t *GrepTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "pattern": { "type": "string", "description": "Regular expression passed to rg --regexp" },
    "path": { "type": "string", "description": "File or directory to search (default: cwd)" },
    "glob": { "type": "string", "description": "Optional glob filter (rg --glob), e.g. \"*.go\"" },
    "caseInsensitive": { "type": "boolean", "description": "Case-insensitive match (rg -i). Default false." },
    "headLimit": { "type": "integer", "description": "Max match lines to return (default 50, hard cap 500)" },
    "context": { "type": "integer", "description": "Lines of context before and after each match (rg -C)" }
  },
  "required": ["pattern"]
}`)
}

type grepInput struct {
	Pattern         string  `json:"pattern"`
	Path            string  `json:"path"`
	Glob            string  `json:"glob"`
	CaseInsensitive bool    `json:"caseInsensitive"`
	HeadLimit       FlexInt `json:"headLimit"`
	Context         FlexInt `json:"context"`
}

const (
	grepDefaultHead = 50
	grepMaxHead     = 500
	grepTimeout     = 30 * time.Second
)

// rgBin is the ripgrep binary. Overridable in tests.
var rgBin = "rg"

func (t *GrepTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	var in grepInput
	if err := json.Unmarshal(input, &in); err != nil {
		return ToolResult{Error: "invalid input: " + err.Error()}, nil
	}
	if strings.TrimSpace(in.Pattern) == "" {
		return ToolResult{Error: "pattern is required"}, nil
	}

	head := int(in.HeadLimit)
	if head <= 0 {
		head = grepDefaultHead
	}
	if head > grepMaxHead {
		head = grepMaxHead
	}

	searchPath := t.cwd
	if in.Path != "" {
		searchPath = resolvePath(t.cwd, in.Path)
	}

	args := []string{
		"--line-number",
		"--with-filename",
		"--color", "never",
		"--no-heading",
		// One past head so we can tell "exactly head" from "truncated".
		"--max-count", fmt.Sprintf("%d", head+1),
	}
	// Align with glob's skipDirNames so agents don't see node_modules hits
	// from grep that glob would have hidden (rg only auto-skips VCS dirs via
	// .gitignore — a tree with no .gitignore still has node_modules).
	for name := range skipDirNames {
		args = append(args, "--glob", "!"+name)
		args = append(args, "--glob", "!"+name+"/**")
	}
	if in.CaseInsensitive {
		args = append(args, "--ignore-case")
	}
	if c := int(in.Context); c > 0 {
		if c > 10 {
			c = 10
		}
		args = append(args, "-C", fmt.Sprintf("%d", c))
	}
	if g := strings.TrimSpace(in.Glob); g != "" {
		args = append(args, "--glob", g)
	}
	args = append(args, "--", in.Pattern, searchPath)

	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, grepTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, rgBin, args...)
	cmd.Dir = t.cwd
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	out := stdout.String()
	errText := strings.TrimSpace(stderr.String())

	// rg exit codes: 0 match, 1 no match, 2 error.
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return ToolResult{Error: "grep timed out after 30s — narrow path/glob or simplify pattern"}, nil
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code := ee.ExitCode()
			if code == 1 {
				return ToolResult{Content: "no matches"}, nil
			}
			if code == 2 || out == "" {
				msg := "rg failed"
				if errText != "" {
					msg += ": " + errText
				} else {
					msg += ": " + err.Error()
				}
				return ToolResult{Error: msg}, nil
			}
			// Unusual non-zero with stdout — still surface output.
		} else if _, lookErr := exec.LookPath(rgBin); lookErr != nil {
			return ToolResult{Error: "rg (ripgrep) not found on PATH — install ripgrep or use bash"}, nil
		} else {
			return ToolResult{Error: "rg failed: " + err.Error()}, nil
		}
	}

	lines := splitKeepNonEmpty(out)
	// With -C, "max-count" limits matches not output lines; still cap
	// displayed lines so one hot file can't flood the context window.
	truncated := false
	if len(lines) > head {
		lines = lines[:head]
		truncated = true
	}
	body := strings.Join(lines, "\n")
	if body == "" {
		return ToolResult{Content: "no matches"}, nil
	}
	if truncated {
		body += fmt.Sprintf("\n\n... (truncated at %d lines — raise headLimit, max %d, or narrow path/glob)", head, grepMaxHead)
	}
	return ToolResult{Content: body}, nil
}

func splitKeepNonEmpty(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
