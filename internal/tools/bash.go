package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"poisson/internal/guard"
)

// BashTool executes bash commands, gated by the bash guard.
type BashTool struct {
	cwd        string
	sandbox    bool
	approvalFn func(command, description, workdir string) bool
}

// NewBashTool creates a bash tool. The approval function is called when a
// command is not auto-safe and not in sandbox mode; if it returns false the
// command is denied. Pass nil to auto-deny all unsafe commands.
func NewBashTool(cwd string, sandbox bool, approvalFn func(command, description, workdir string) bool) *BashTool {
	return &BashTool{cwd: cwd, sandbox: sandbox, approvalFn: approvalFn}
}

func (t *BashTool) Name() string { return "bash" }

func (t *BashTool) Description() string {
	return "Execute a bash command. 'description' (REQUIRED) must be a short one-line purpose explaining what the command does — the user sees it in approval prompts for gated commands. Safe commands run automatically; others require approval."
}

func (t *BashTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "command": { "type": "string" },
    "description": { "type": "string", "description": "Short description of what the command does" },
    "workdir": { "type": "string", "description": "Working directory (default: cwd)" },
    "timeout": { "type": "integer", "description": "Timeout in seconds (default: 120)" }
  },
  "required": ["command", "description"]
}`)
}

type bashInput struct {
	Command     string `json:"command"`
	Description string `json:"description"`
	Workdir     string `json:"workdir"`
	Timeout     int    `json:"timeout"`
}

type bashOutput struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exitCode"`
}

func (t *BashTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	var in bashInput
	if err := json.Unmarshal(input, &in); err != nil {
		return ToolResult{Error: "invalid input: " + err.Error()}, nil
	}
	if in.Command == "" {
		return ToolResult{Error: "command is required"}, nil
	}

	// Guard: unless sandbox, classify the command.
	if !t.sandbox {
		safe, reason := guard.Classify(in.Command)
		if !safe {
			// Gated bash call requires description (per PR-24); reject at tool layer if missing.
			if in.Description == "" {
				return ToolResult{Error: "description is required"}, nil
			}
			// Use provided description; fallback from guard reason if somehow empty at approval time.
			purpose := in.Description
			if purpose == "" {
				purpose = reason
				if purpose == "" {
					purpose = "(no description provided)"
				}
			}
			approved := false
			if t.approvalFn != nil {
				approved = t.approvalFn(in.Command, purpose, in.Workdir)
			}
			if !approved {
				return ToolResult{Error: fmt.Sprintf("command denied (not safe: %s)", reason)}, nil
			}
		}
	}

	// Resolve working directory.
	dir := t.cwd
	if in.Workdir != "" {
		if filepath.IsAbs(in.Workdir) {
			dir = in.Workdir
		} else {
			dir = filepath.Join(t.cwd, in.Workdir)
		}
	}

	// Determine timeout.
	timeoutSec := 120
	if in.Timeout > 0 {
		timeoutSec = in.Timeout
	}
	timeoutDur := time.Duration(timeoutSec) * time.Second

	// Create a child context with timeout.
	childCtx, cancel := context.WithTimeout(ctx, timeoutDur)
	defer cancel()

	cmd := exec.CommandContext(childCtx, "bash", "-c", in.Command)
	if dir != "" {
		cmd.Dir = dir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
			if stderr.Len() == 0 {
				stderr.WriteString(err.Error())
			}
		}
	}

	out := bashOutput{
		Stdout:   sanitizeToolText(stdout.String()),
		Stderr:   sanitizeToolText(stderr.String()),
		ExitCode: exitCode,
	}
	data, _ := json.Marshal(out)
	return ToolResult{Content: string(data)}, nil
}

// resolvePath joins a path with the tool's cwd if it is relative.
func resolvePath(cwd, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(cwd, p)
}

// envOrDefault reads an env var or returns the default.
func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// itoa is a small helper to avoid importing strconv in callers.
func itoa(n int) string { return strconv.Itoa(n) }
