package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// SearchTool wraps ripgrep (rg) for content search.
type SearchTool struct {
	cwd string
}

func NewSearchTool(cwd string) *SearchTool { return &SearchTool{cwd: cwd} }

func (t *SearchTool) Name() string { return "search" }

func (t *SearchTool) Description() string {
	return "Search file contents using ripgrep. Returns matching lines with file paths and line numbers; before/after add surrounding context lines (like grep -B/-A). Prefer this over bash `grep`/`rg` when searching text is the whole command — skips bash's risk-classification step entirely."
}

func (t *SearchTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "pattern": { "type": "string", "description": "Regex pattern" },
    "path": { "type": "string", "description": "One or more files/directories to search, space-separated like ripgrep (e.g. 'internal cmd docs'). Default: cwd." },
    "glob": { "type": "string", "description": "File glob filter (e.g. '*.go')" },
    "ignore_case": { "type": "boolean" },
    "before": { "type": "integer", "description": "Lines of context to show before each match (like grep -B)" },
    "after": { "type": "integer", "description": "Lines of context to show after each match (like grep -A)" },
    "max_results": { "type": "integer", "default": 100 }
  },
  "required": ["pattern"]
}`)
}

type searchInput struct {
	Pattern    string  `json:"pattern"`
	Path       string  `json:"path"`
	Glob       string  `json:"glob"`
	IgnoreCase bool    `json:"ignore_case"`
	Before     FlexInt `json:"before"`
	After      FlexInt `json:"after"`
	MaxResults FlexInt `json:"max_results"`
}

// rgMatch represents a single match from rg --json output.
type rgMatch struct {
	Type string `json:"type"`
	Data struct {
		Path struct {
			Text string `json:"text"`
		} `json:"path"`
		LineNumber int `json:"line_number"`
		Lines      struct {
			Text string `json:"text"`
		} `json:"lines"`
	} `json:"data"`
}

func (t *SearchTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	var in searchInput
	if err := json.Unmarshal(input, &in); err != nil {
		return ToolResult{Error: "invalid input: " + err.Error()}, nil
	}
	if in.Pattern == "" {
		return ToolResult{Error: "pattern is required"}, nil
	}

	maxResults := int(in.MaxResults)
	if maxResults <= 0 {
		maxResults = 100
	}

	// rg accepts multiple paths (rg PATTERN path1 path2 ...) and the model uses
	// that convention, so treat `path` as whitespace-separated paths rather than a
	// single one. Falls back to cwd when empty.
	paths := strings.Fields(in.Path)
	if len(paths) == 0 {
		paths = []string{t.cwd}
	}

	// "--" ends option parsing before pattern/paths, and "--glob=" ties the
	// glob value to its flag with no separate token — otherwise a pattern,
	// path, or glob value starting with "-" (e.g. "--pre=/tmp/x.sh") would be
	// parsed as an rg flag instead of a literal, letting the model run
	// arbitrary commands via rg's --pre preprocessor.
	args := []string{"--json", "--max-count", strconv.Itoa(maxResults)}
	if in.IgnoreCase {
		args = append(args, "-i")
	}
	if in.Before > 0 {
		args = append(args, "-B", strconv.Itoa(int(in.Before)))
	}
	if in.After > 0 {
		args = append(args, "-A", strconv.Itoa(int(in.After)))
	}
	if in.Glob != "" {
		args = append(args, "--glob="+in.Glob)
	}
	args = append(args, "--", in.Pattern)
	args = append(args, paths...)

	cmd := exec.CommandContext(ctx, "rg", args...)
	if t.cwd != "" {
		cmd.Dir = t.cwd
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return ToolResult{Error: "cannot start rg: " + err.Error()}, nil
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return ToolResult{Error: "cannot start rg: " + err.Error()}, nil
	}

	var b strings.Builder
	count := 0
	truncated := false
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var m rgMatch
		if err := json.Unmarshal(line, &m); err != nil {
			continue
		}
		// With -A/-B/-C set, rg's --json stream also carries "context" lines
		// around each match and a "context_break" between non-adjacent groups.
		// Mirror grep's own convention ("-" separator for context, "--" between
		// groups) so the shape is one models already know how to read.
		switch m.Type {
		case "match":
			filePath := m.Data.Path.Text
			text := strings.TrimRight(m.Data.Lines.Text, "\n")
			b.WriteString(fmt.Sprintf("%s:%d: %s\n", filePath, m.Data.LineNumber, text))
			count++
			if count >= maxResults {
				truncated = true
			}
		case "context":
			filePath := m.Data.Path.Text
			text := strings.TrimRight(m.Data.Lines.Text, "\n")
			b.WriteString(fmt.Sprintf("%s-%d- %s\n", filePath, m.Data.LineNumber, text))
		case "context_break":
			b.WriteString("--\n")
		default:
			continue
		}
		if truncated {
			break
		}
	}
	earlyStop := truncated || scanner.Err() != nil
	if earlyStop && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()

	scanErr := scanner.Err()
	if scanErr != nil {
		if count == 0 {
			return ToolResult{Error: "search output unreadable: " + scanErr.Error()}, nil
		}
		b.WriteString(fmt.Sprintf("\n... (scanner error: %v)\n", scanErr))
	}
	if count == 0 {
		if waitErr != nil && strings.TrimSpace(stderr.String()) != "" {
			return ToolResult{Error: strings.TrimSpace(stderr.String())}, nil
		}
		return ToolResult{Content: "no matches found"}, nil
	}
	if truncated {
		b.WriteString(fmt.Sprintf("... (truncated at %d matches)\n", maxResults))
	}
	return ToolResult{Content: b.String()}, nil
}
