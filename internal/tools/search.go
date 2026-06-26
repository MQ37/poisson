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
	return "Search file contents using ripgrep. Returns matching lines with file paths and line numbers."
}

func (t *SearchTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "pattern": { "type": "string", "description": "Regex pattern" },
    "path": { "type": "string", "description": "Directory or file to search (default: cwd)" },
    "glob": { "type": "string", "description": "File glob filter (e.g. '*.go')" },
    "ignore_case": { "type": "boolean" },
    "max_results": { "type": "integer", "default": 100 }
  },
  "required": ["pattern"]
}`)
}

type searchInput struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path"`
	Glob       string `json:"glob"`
	IgnoreCase bool   `json:"ignore_case"`
	MaxResults int    `json:"max_results"`
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
		Submatches []struct {
			Match struct {
				Text string `json:"text"`
			} `json:"match"`
		} `json:"submatches"`
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

	maxResults := in.MaxResults
	if maxResults <= 0 {
		maxResults = 100
	}

	searchPath := in.Path
	if searchPath == "" {
		searchPath = t.cwd
	}

	args := []string{"--json", "--max-count", strconv.Itoa(maxResults), in.Pattern, searchPath}
	if in.IgnoreCase {
		args = append([]string{"-i"}, args...)
	}
	if in.Glob != "" {
		args = append([]string{"--glob", in.Glob}, args...)
	}

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
		if m.Type != "match" {
			continue
		}
		filePath := m.Data.Path.Text
		// Make path relative to cwd if possible.
		text := strings.TrimRight(m.Data.Lines.Text, "\n")
		b.WriteString(fmt.Sprintf("%s:%d: %s\n", filePath, m.Data.LineNumber, text))
		count++
		if count >= maxResults {
			truncated = true
			break
		}
	}
	if truncated && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()

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
