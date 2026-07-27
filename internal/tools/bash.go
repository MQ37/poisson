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
//
// Within one agent session the tool keeps a RAM-only sticky cwd + environment
// so `cd` / `export` survive across calls (Grok-style continuity without a
// persistent shell process). Subagents get their own BashTool via
// BuildRegistry, so sticky state is never shared with the parent. Nothing is
// written to the session DB — restart/resume starts clean at session cwd.
type BashTool struct {
	cwd        string // session workspace root (fallback when sticky empty)
	sandbox    bool
	approvalFn func(ctx context.Context, command, description, workdir string) (bool, string)
	sticky     bashSticky
}

// NewBashTool creates a bash tool. The approval function is called when a
// command is not auto-safe and not in sandbox mode; if it returns false the
// command is denied. Pass nil to auto-deny all unsafe commands.
func NewBashTool(cwd string, sandbox bool, approvalFn func(ctx context.Context, command, description, workdir string) (bool, string)) *BashTool {
	return &BashTool{cwd: cwd, sandbox: sandbox, approvalFn: approvalFn}
}

func (t *BashTool) Name() string { return "bash" }

func (t *BashTool) Description() string {
	return "Execute a bash command. 'description' (REQUIRED) must be a short one-line purpose explaining what the command does — the user sees it in approval prompts for gated commands. A deterministic guard auto-approves read-only, side-effect-free commands (ls, cat, grep/rg, find, git status/diff/log, ...) with no approval step at all; gated commands classified as low risk by the LLM also run automatically; medium/high/unknown require human approval. Prefer dedicated tools when they cover the job: read (not cat/head/tail/sed -n), grep (not rg/grep for content search), glob (not find -name), edit/write (not sed -i/awk redirect). Plain cat/head/tail/sed -n still runs (not refused) but comes back with a hint nudging you to 'read' next time — skips the approval gate, supports offset/limit. For several independent tool ops in one step use batch (not a bash pipeline of the same). Cwd and environment stick across bash calls in this session only (RAM; lost on restart; subagents isolated) — cd/export once and later calls see them. Optional workdir overrides sticky cwd for one call and then updates sticky to the resulting pwd."
}

func (t *BashTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "command": { "type": "string" },
    "description": { "type": "string", "description": "Short description of what the command does" },
    "workdir": { "type": "string", "description": "Working directory for this call (default: sticky cwd from prior bash in this session, else session cwd). Absolute or relative to session cwd. Updates sticky cwd after the call." },
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
	// workdir param instead of a leading `cd DIR &&`, or a dedicated tool
	// (read) instead of cat/head/tail/sed -n (see dedicatedToolHint). The
	// command still runs and its real output is returned either way — this
	// is advisory only. Empty when nothing applies.
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
	if strings.TrimSpace(in.Description) == "" {
		return ToolResult{Error: "description is required — call bash again with a short one-line 'description' explaining what this command does (the user sees it in approval prompts)."}, nil
	}

	// Refuse spawning poisson itself with --yolo / -p --yolo. --yolo is a
	// human footgun for headless scripting; an agent that shells out to
	// `px -p --yolo ...` would silently auto-approve every nested command
	// and defeat this process's own approval gate. Checked before the
	// sandbox short-circuit so even test/sandbox callers can't smuggle it.
	if invokesPoissonYolo(in.Command) {
		return ToolResult{Error: "blocked: refusing to run poisson/px with --yolo (nested auto-approve). Run --yolo yourself from a real shell if you need it."}, nil
	}

	// Serialize against sticky state for this session's BashTool. Parallel
	// bash tool_uses would otherwise race cwd/env snapshots.
	t.sticky.lock()
	defer t.sticky.unlock()

	stickyCwd, stickyEnv := t.sticky.cwd, t.sticky.env
	dir := stickyStartDir(t.cwd, stickyCwd, in.Workdir)

	// A stale dir (sticky cwd's directory got deleted/unmounted since the
	// last call, or an explicit workdir that doesn't exist) must not reach
	// exec.Cmd: Cmd.SysProcAttr is set below (needed for process-group
	// kill), which disables Go's own friendly chdir-existence pre-check
	// (os/exec_posix.go only stats Dir when Sys == nil). Without this check
	// a missing dir fails deep inside the forked child's chdir(), and Go
	// can only report it as "fork/exec <bash path>: no such file or
	// directory" -- blaming the bash binary instead of the real cause.
	// Worse, since the wrapper script never starts, readStickyDump never
	// runs and t.sticky.cwd is never corrected -- every later call would
	// repeat the same failure forever. Catch it here and self-heal instead.
	var hints []string
	if dir != "" {
		if info, statErr := os.Stat(dir); statErr != nil || !info.IsDir() {
			bad := dir
			dir = t.cwd
			if in.Workdir != "" {
				hints = append(hints, fmt.Sprintf("workdir %q does not exist; ran in session root %q instead.", bad, t.cwd))
			} else {
				t.sticky.cwd = ""
				hints = append(hints, fmt.Sprintf("sticky working directory %q no longer exists (deleted/unmounted); reset to session root %q.", bad, t.cwd))
			}
		}
	}

	// Every command is gated: approvalFn runs the LLM risk classifier, which
	// auto-approves only an LLM "low" and routes everything else (including a
	// failed classification) to the human. Sandbox mode skips the gate.
	if !t.sandbox {
		approved, reason := false, ""
		if t.approvalFn != nil {
			approved, reason = t.approvalFn(ctx, in.Command, in.Description, dir)
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

	// State dump files: wrapper writes pwd + env -0 here after the user
	// command so we can update sticky without scraping command output.
	// User command lives in cmdFile and is eval'd in the same shell (nested
	// bash -c would discard cd/export).
	stateDir, err := os.MkdirTemp("", "poisson-bash-sticky-*")
	if err != nil {
		return ToolResult{Error: "cannot create sticky state dir: " + err.Error()}, nil
	}
	defer os.RemoveAll(stateDir)
	cmdFile := filepath.Join(stateDir, "cmd")
	cwdFile := filepath.Join(stateDir, "cwd")
	envFile := filepath.Join(stateDir, "env")
	if err := os.WriteFile(cmdFile, []byte(in.Command), 0o600); err != nil {
		return ToolResult{Error: "cannot write sticky cmd file: " + err.Error()}, nil
	}

	wrapped := wrapBashForSticky(cmdFile, cwdFile, envFile)
	cmd := exec.CommandContext(childCtx, "bash", "-c", wrapped)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = envForCmd(stickyEnv)
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

	err = cmd.Run()
	exitCode := 0
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

	// Always try to refresh sticky from the dump when the wrapper got far
	// enough to write it — including failed commands (cd good; false should
	// still leave sticky in good). Missing dump (killed early) keeps prior.
	if newCwd, newEnv, dumpErr := readStickyDump(cwdFile, envFile); dumpErr == nil {
		t.sticky.cwd = newCwd
		t.sticky.env = newEnv
	}

	if !t.sandbox {
		if h := cdWorkdirHint(in.Command, in.Workdir); h != "" {
			hints = append(hints, h)
		}
		if h := dedicatedToolHint(in.Command); h != "" {
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

// StickyCwd returns the current sticky working directory (empty if still at
// the session default). Test/helper accessor; not part of the tool API.
func (t *BashTool) StickyCwd() string {
	t.sticky.lock()
	defer t.sticky.unlock()
	return t.sticky.cwd
}

// isCdSegment reports whether a command segment is just `cd <path>` (no
// further flags/args).
func isCdSegment(seg string) bool {
	fields := strings.Fields(strings.TrimSpace(seg))
	return len(fields) == 2 && fields[0] == "cd"
}

// poissonBinNames are argv0 forms that mean "this process's own binary"
// (or a common install name). Matched after filepath.Base so
// /usr/local/bin/px and ./px both count.
var poissonBinNames = map[string]bool{
	"px": true, "poisson": true,
}

// invokesPoissonYolo reports whether any segment of command runs poisson/px
// with a --yolo flag. Used to stop the agent from nesting a headless
// auto-approve child under itself (`px -p --yolo "rm -rf /"` etc.).
//
// Not a full shell parse: looks at tokens the same way the guard does
// (quotes honored via guard.Tokenize) and flags any segment whose command
// name is px/poisson and that carries a --yolo token anywhere in argv.
// Wrapper prefixes (sudo, env, timeout, sh -c 'px --yolo ...') are walked
// the same way agent risk detectors walk them — via a second pass over
// shell-script arguments.
func invokesPoissonYolo(command string) bool {
	for _, seg := range guard.Segments(command) {
		if segmentInvokesPoissonYolo(seg) {
			return true
		}
	}
	return false
}

func segmentInvokesPoissonYolo(seg string) bool {
	tokens := guard.Tokenize(seg)
	if segmentTokensHavePoissonYolo(tokens) {
		return true
	}
	// One level into a shell wrapper: sh -c 'px --yolo ...'
	for i, tok := range tokens {
		if !guard.IsShellInterpreter(guard.NormalizeToken(tok)) {
			continue
		}
		for _, arg := range tokens[i+1:] {
			script := strings.Trim(arg, `'"`)
			if invokesPoissonYolo(script) {
				return true
			}
		}
	}
	return false
}

func segmentTokensHavePoissonYolo(tokens []string) bool {
	hasBin, hasYolo := false, false
	for _, tok := range tokens {
		// Strip a leading "./" or path so "./px" / "/usr/bin/px" match.
		base := strings.ToLower(filepath.Base(strings.Trim(tok, "'\"")))
		if poissonBinNames[base] {
			hasBin = true
		}
		// Exact --yolo only (not --yolo-something): the flag is a bare
		// boolean in cmd/px/main.go with no =value form.
		if strings.Trim(tok, "'\"") == "--yolo" {
			hasYolo = true
		}
	}
	return hasBin && hasYolo
}

// cdWorkdirHint nudges the model toward the workdir param when the command
// is a plain `cd DIR && rest` chain — same effect, without repeating the
// path (and re-paying its tokens) on every call. With sticky cwd a plain
// `cd DIR` (no &&) already persists; the hint still helps for one-liners
// that prefix every command with cd.
func cdWorkdirHint(command, workdir string) string {
	if workdir != "" {
		return ""
	}
	segs := guard.Segments(command)
	if len(segs) < 2 || !isCdSegment(segs[0]) {
		return ""
	}
	dir := strings.Fields(strings.TrimSpace(segs[0]))[1]
	return fmt.Sprintf("this command starts with 'cd %s \u0026\u0026' — pass workdir: %q instead next time (same effect, fewer tokens), or rely on sticky cwd after a plain cd.", dir, dir)
}

// dedicatedToolHint reports (advisory only — the command still runs either
// way) when a command is plainly just a stand-in for read — that tool still
// beats an equivalent bash call (offset/limit line ranges, image decode, no
// shell parsing, skips the approval gate entirely). Never blocks: a command
// this fires on has already been approved and executed by the time the hint
// is attached to the result, same as cdWorkdirHint.
//
// grep/glob exist as dedicated tools too, but plain rg/grep/find/ls get the
// same soft nudge via the tool description and system prompt rather than a
// dedicated check here — so multi-step shell pipelines stay legal. Only
// fires when the command boils down to a single segment (after stripping a
// leading `cd DIR &&`, see cdWorkdirHint) with no redirects.
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
