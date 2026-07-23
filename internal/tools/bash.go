package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mq37/poisson/internal/guard"
)

// BashTool executes bash commands, gated by the LLM risk classifier via approvalFn.
type BashTool struct {
	cwd        string
	sandbox    bool
	approvalFn func(ctx context.Context, command, description, workdir string) (bool, string)
}

// NewBashTool creates a bash tool. The approval function is called when a
// command is not auto-safe and not in sandbox mode; if it returns false the
// command is denied. Pass nil to auto-deny all unsafe commands.
func NewBashTool(cwd string, sandbox bool, approvalFn func(ctx context.Context, command, description, workdir string) (bool, string)) *BashTool {
	return &BashTool{cwd: cwd, sandbox: sandbox, approvalFn: approvalFn}
}

func (t *BashTool) Name() string { return "bash" }

func (t *BashTool) Description() string {
	return "Execute a bash command. 'description' (REQUIRED) must be a short one-line purpose explaining what the command does — the user sees it in approval prompts for gated commands. A deterministic guard auto-approves read-only, side-effect-free commands (ls, cat, grep/rg, find, git status/diff/log, ...) with no approval step at all; gated commands classified as low risk by the LLM also run automatically; medium/high/unknown require human approval. A command that is just cat/head/tail/sed -n standing in for the read tool is refused — use that tool instead (offset/limit, image support, skips the gate too)."
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
	Command     string  `json:"command"`
	Description string  `json:"description"`
	Workdir     string  `json:"workdir"`
	Timeout     FlexInt `json:"timeout"`
}

type bashOutput struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exitCode"`
	// Hint is poisson-injected advisory text (not part of the command's real
	// output) nudging the model toward a cheaper pattern next time — e.g. the
	// workdir param instead of a leading `cd DIR &&`. A command that is just
	// a stand-in for a dedicated tool doesn't reach here at all — it's
	// refused before execution (see dedicatedToolHint). Empty when nothing
	// applies.
	Hint string `json:"hint,omitempty"`
}

func (t *BashTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	var in bashInput
	if err := json.Unmarshal(input, &in); err != nil {
		return ToolResult{Error: "invalid input: " + err.Error()}, nil
	}
	if in.Command == "" {
		return ToolResult{Error: "command is required"}, nil
	}

	// A command that is plainly just a stand-in for read is refused
	// outright, before approval or execution: that tool does the same job
	// for less (no approval round trip, offset/limit, image decode) and
	// returning real output here would only reward the wrong habit. Sandbox
	// mode skips this too (see dedicatedToolHint).
	if !t.sandbox {
		if h := dedicatedToolHint(in.Command); h != "" {
			return ToolResult{Error: "blocked: " + h}, nil
		}
	}

	// Resolve working directory before guard (sensitive cwd affects approval).
	dir := t.cwd
	if in.Workdir != "" {
		if filepath.IsAbs(in.Workdir) {
			dir = in.Workdir
		} else {
			dir = filepath.Join(t.cwd, in.Workdir)
		}
	}

	// Every command is gated: approvalFn runs the LLM risk classifier, which
	// auto-approves only an LLM "low" and routes everything else (including a
	// failed classification) to the human. Sandbox mode skips the gate.
	if !t.sandbox {
		purpose := in.Description
		if purpose == "" {
			purpose = "(no description provided)"
		}
		approved, reason := false, ""
		if t.approvalFn != nil {
			approved, reason = t.approvalFn(ctx, in.Command, purpose, dir)
		}
		if !approved {
			msg := "command rejected by user"
			if reason = strings.TrimSpace(reason); reason != "" {
				msg += " - reason: " + reason
			}
			return ToolResult{Error: msg}, nil
		}
	}

	// Determine timeout.
	timeoutSec := 120
	if in.Timeout > 0 {
		timeoutSec = int(in.Timeout)
	}
	timeoutDur := time.Duration(timeoutSec) * time.Second

	// Create a child context with timeout.
	childCtx, cancel := context.WithTimeout(ctx, timeoutDur)
	defer cancel()

	cmd := exec.CommandContext(childCtx, "bash", "-c", in.Command)
	if dir != "" {
		cmd.Dir = dir
	}
	// Run in its own process group and kill the whole group on timeout/cancel,
	// otherwise only the bash shell dies and its spawned children are orphaned.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 2 * time.Second

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	var hints []string
	if err != nil {
		switch {
		case errors.Is(err, exec.ErrWaitDelay):
			// The command itself exited successfully; only a child it left
			// running past its own exit (e.g. `server &`) still held the
			// stdout/stderr pipe open, so Go force-closed it after WaitDelay
			// instead of reporting a real failure. Report success, not -1 —
			// see https://pkg.go.dev/os/exec#Cmd.WaitDelay.
			hints = append(hints, "command exited successfully but left a background process running past its own exit (e.g. 'cmd &'); output above may be truncated at that point.")
		default:
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				exitCode = exitErr.ExitCode()
				break
			}
			exitCode = -1
			if stderr.Len() == 0 {
				stderr.WriteString(err.Error())
			}
		}
	}

	if !t.sandbox {
		if h := cdWorkdirHint(in.Command, in.Workdir); h != "" {
			hints = append(hints, h)
		}
	}

	out := bashOutput{
		Stdout:   sanitizeToolText(stdout.String()),
		Stderr:   sanitizeToolText(stderr.String()),
		ExitCode: exitCode,
		Hint:     strings.Join(hints, " "),
	}
	data, _ := json.Marshal(out)
	return ToolResult{Content: string(data)}, nil
}

// isCdSegment reports whether a command segment is just `cd <path>` (no
// further flags/args).
func isCdSegment(seg string) bool {
	fields := strings.Fields(strings.TrimSpace(seg))
	return len(fields) == 2 && fields[0] == "cd"
}

// cdWorkdirHint nudges the model toward the workdir param when the command
// is a plain `cd DIR && rest` chain — same effect, without repeating the
// path (and re-paying its tokens) on every call.
func cdWorkdirHint(command, workdir string) string {
	if workdir != "" {
		return ""
	}
	segs := guard.Segments(command)
	if len(segs) < 2 || !isCdSegment(segs[0]) {
		return ""
	}
	dir := strings.Fields(strings.TrimSpace(segs[0]))[1]
	return fmt.Sprintf("this command starts with 'cd %s \u0026\u0026' — pass workdir: %q instead next time (same effect, fewer tokens).", dir, dir)
}

// dedicatedToolHint reports (and, in Execute, blocks) when a command is
// plainly just a stand-in for read — the only tool left that still beats an
// equivalent bash call (offset/limit line ranges, image decode, no shell
// parsing) — skipping the approval gate entirely (see its description).
// ls/grep/rg/find have no such dedicated tool anymore: they're plain bash,
// gated by the deterministic guard fast path instead (see
// agent.WrapRiskGatedApproval). Only fires when the command boils down to a
// single segment (after stripping a leading `cd DIR &&`, see
// cdWorkdirHint) with no redirects, so it won't fire on multi-step
// pipelines that legitimately combine several commands.
func dedicatedToolHint(command string) string {
	segs := guard.Segments(command)
	i := 0
	for i < len(segs)-1 && isCdSegment(segs[i]) {
		i++
	}
	if len(segs) == 0 || i != len(segs)-1 {
		return ""
	}
	seg := strings.TrimSpace(segs[i])
	if strings.ContainsAny(seg, "><") {
		return ""
	}
	fields := strings.Fields(seg)
	if len(fields) == 0 {
		return ""
	}
	switch fields[0] {
	case "cat", "head", "tail":
		if len(fields) > 3 {
			return ""
		}
		return "this reads a file — prefer the 'read' tool (skips the approval gate, supports offset/limit)."
	case "sed":
		if len(fields) >= 2 && fields[1] == "-n" {
			return "prefer the 'read' tool with offset/limit for line ranges — skips the approval gate."
		}
	}
	return ""
}

// resolvePath joins a path with the tool's cwd if it is relative.
func resolvePath(cwd, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(cwd, p)
}

// requireDir errors out if p doesn't exist or isn't a directory. Callers that
// walk p (filepath.Walk) must check this themselves first: Walk invokes its
// callback once with the root's own stat error, and a callback that returns
// nil for it (to keep walking past unrelated errors deeper in the tree, e.g.
// a permission-denied subdir) makes Walk swallow that root error entirely —
// a nonexistent base directory then silently looks identical to a real
// directory with zero matching entries, instead of surfacing as an error.
func requireDir(p string) error {
	info, err := os.Stat(p)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", p)
	}
	return nil
}

// sensitivePathDenyMsg builds the error a file tool returns when access to a
// sensitive path is blocked or denied approval. reason is the guard's
// sensitivity classification; denyReason is the optional human-supplied
// explanation from the approval prompt.
func sensitivePathDenyMsg(reason, denyReason string) string {
	msg := "blocked: " + reason
	if denyReason = strings.TrimSpace(denyReason); denyReason != "" {
		msg += " - reason: " + denyReason
	}
	return msg
}
