// Package subagent spawns child Poisson processes for isolated task execution.
package subagent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"

	"github.com/mq37/poisson/internal/provider"
)

// SpawnInput holds the parameters for spawning a child Poisson process.
type SpawnInput struct {
	Task      string
	Cwd       string
	SessionID string
	Name      string
	Provider  string
	Model     string
	Effort    string
	// ClassifierModel is the bash-risk classifier model the parent session is
	// using, so a /classifier-model pin applies to the whole px instance
	// rather than stopping at the process boundary (children otherwise see
	// only the config default). Empty means "let the child resolve it".
	ClassifierModel string
	NoSkills        bool // mirrors the parent's SkillsEnabled(): true disables skills in the child too
	ExtraEnv        []string
	DBPath          string // ephemeral DB path for the child (empty = parent's DB)
}

// ChildProcess wraps a spawned Poisson child process.
type ChildProcess struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	stdinMu sync.Mutex
}

// ChildEvent is a JSON event emitted by the child on stdout.
type ChildEvent struct {
	Type          string          `json:"type"`
	Text          string          `json:"text,omitempty"`
	Tool          string          `json:"tool,omitempty"`
	ToolInput     json.RawMessage `json:"tool_input,omitempty"`
	Result        string          `json:"result,omitempty"`
	Command       string          `json:"command,omitempty"`
	Description   string          `json:"description,omitempty"`
	Cwd           string          `json:"cwd,omitempty"`
	Agent         string          `json:"agent,omitempty"`
	Risk          string          `json:"risk,omitempty"`
	Approved      bool            `json:"approved,omitempty"`
	Success       bool            `json:"success,omitempty"`
	Turns         int             `json:"turns,omitempty"`
	ContextTokens int             `json:"contextTokens,omitempty"`
	ContextWindow int             `json:"contextWindow,omitempty"`
	Error         string          `json:"error,omitempty"`
	// Usage is the child's cumulative token usage across its whole run so
	// far (see Agent.CumulativeUsage) — sent on "tool" and "done" events so
	// the parent can roll subagent spend into its own session cost even if
	// the parent's turn is cancelled before the child sends "done" (see
	// SubagentTool.Execute). nil means no usage to report yet.
	Usage *provider.Usage `json:"usage,omitempty"`
	// TokensPerSec is the child's own average output tokens/sec for its most
	// recently completed streaming round (see agent.OutputInferenceSpeed),
	// sent on a "speed" event — lets the parent's subagent widget show the
	// child's inference speed the same way the main conversation shows it
	// for its own rounds. Zero/omitted means nothing measurable to report.
	TokensPerSec float64 `json:"tokensPerSec,omitempty"`
	// Cost is the child's own cumulative dollar cost for every api_calls row
	// it has recorded so far, priced by the child against the actual model of
	// each call. Sent everywhere Usage is. The parent records it verbatim
	// rather than re-pricing Usage as one lump at the child's main model,
	// which would misprice any round that ran on a different model — notably
	// bash-risk classification under a /classifier-model pin.
	Cost float64 `json:"cost,omitempty"`
	// OutputTokens is that same round's exact output-token count, sent
	// alongside TokensPerSec so the parent can keep a token-weighted running
	// average across the child's rounds instead of showing one raw last-round
	// sample (see SubagentTool.Execute). This is the weight the main
	// conversation's header average already uses for its own rounds — see
	// tui.scrollback.avgTokensPerSec. Only meaningful on a "speed" event.
	OutputTokens int `json:"outputTokens,omitempty"`
}

// lookupExecutable resolves the binary Spawn execs as the child. A package
// var (not a hardcoded os.Executable() call) so tests can point it at a
// small fake "child" script instead of the real px binary — exercising
// Spawn's actual exec/pipe/env machinery end-to-end with zero real LLM
// calls, instead of only ever running against the live binary (which
// spawn_test.go previously did for nothing but Reap()).
var lookupExecutable = os.Executable

// SetLookupExecutableForTest overrides the binary Spawn execs as the child,
// returning a restore func. Test-only: lets other packages' tests (e.g.
// internal/tools's SubagentTool.Execute tests) run Spawn end-to-end against
// a small fake "child" script instead of the real px binary, with zero real
// LLM calls.
func SetLookupExecutableForTest(path string) (restore func()) {
	old := lookupExecutable
	lookupExecutable = func() (string, error) { return path, nil }
	return func() { lookupExecutable = old }
}

// buildSpawnArgs returns the child process's argv (excluding argv[0]) for
// input. Pulled out of Spawn as a pure function so the argument-construction
// logic — exactly the kind of propagation bug already found once this
// session ("subagent silently falls back to hardcoded model") — is directly
// unit-testable without spawning any process at all.
func buildSpawnArgs(input SpawnInput) []string {
	args := []string{"--json"}
	if input.NoSkills {
		args = append(args, "--no-skills")
	}
	args = append(args, "--session", input.SessionID)
	if input.Task != "" {
		args = append(args, "--", input.Task)
	}
	return args
}

// buildSpawnEnv returns the full environment for the child process: the
// current process's environment (so PATH/HOME/etc. are available) plus
// input.ExtraEnv, plus the POISSON_SUBAGENT_* variables runChildMode reads
// to learn its provider/model/effort/name/db settings. Pulled out of Spawn
// as a pure function for the same reason as buildSpawnArgs.
//
// Deliberately does NOT set any "sandbox" env flag: an ambient
// POISSON_SANDBOX=/IS_SANDBOX= used to short-circuit the bash guard to
// always-safe, which is the opposite of isolation. Child approvals still
// go through the parent broker.
func buildSpawnEnv(input SpawnInput) []string {
	env := append(os.Environ(), input.ExtraEnv...)
	env = append(env, "POISSON_SUBAGENT_CHILD=1")
	if input.Provider != "" {
		env = append(env, fmt.Sprintf("POISSON_SUBAGENT_PROVIDER=%s", input.Provider))
	}
	if input.Model != "" {
		env = append(env, fmt.Sprintf("POISSON_SUBAGENT_MODEL=%s", input.Model))
	}
	if input.ClassifierModel != "" {
		env = append(env, fmt.Sprintf("POISSON_SUBAGENT_CLASSIFIER_MODEL=%s", input.ClassifierModel))
	}
	if input.Effort != "" {
		env = append(env, fmt.Sprintf("POISSON_SUBAGENT_EFFORT=%s", input.Effort))
	}
	if input.Name != "" {
		env = append(env, fmt.Sprintf("POISSON_SUBAGENT_NAME=%s", input.Name))
	}
	if input.DBPath != "" {
		env = append(env, fmt.Sprintf("POISSON_SUBAGENT_DB=%s", input.DBPath))
	}
	return env
}

// Spawn starts a child Poisson process in JSON output mode.
func Spawn(input SpawnInput) (*ChildProcess, error) {
	// The child is flagged via POISSON_SUBAGENT_CHILD below; runChildMode then
	// builds the registry with Child:true, which grants every tool except
	// subagent (so subagents cannot spawn subagents).
	args := buildSpawnArgs(input)

	bin, err := lookupExecutable()
	if err != nil {
		bin = "px"
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = input.Cwd
	cmd.Env = buildSpawnEnv(input)

	// Run the child in its own process group so Reap() can kill the whole tree
	// (the child plus any bash grandchildren it spawned) on Ctrl+C — otherwise
	// killing only the child orphans its subprocesses, leaving them running.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start child: %w", err)
	}

	return &ChildProcess{
		cmd:    cmd,
		stdin:  stdin,
		stdout: bufio.NewReader(stdout),
	}, nil
}

// ReadEvent reads one JSON line from the child's stdout.
func (c *ChildProcess) ReadEvent() (*ChildEvent, error) {
	line, err := c.stdout.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, nil
	}
	var ev ChildEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return nil, fmt.Errorf("parse child event: %w", err)
	}
	return &ev, nil
}

// SendApproval writes an approval response to the child's stdin. reason is an
// optional human-supplied explanation when denying (empty when allowed).
func (c *ChildProcess) SendApproval(approved bool, reason string) error {
	data, err := json.Marshal(map[string]interface{}{
		"type":     "approval_response",
		"approved": approved,
		"reason":   reason,
	})
	if err != nil {
		return err
	}
	_, err = c.stdin.Write(append(data, '\n'))
	return err
}

// SendExpedite asks the child to wrap up early by writing an expedite message
// to its stdin. The child injects a finish-now nudge at its next micro-turn
// boundary and returns partial results. Thread-safe.
func (c *ChildProcess) SendExpedite() error {
	c.stdinMu.Lock()
	defer c.stdinMu.Unlock()
	data, err := json.Marshal(map[string]interface{}{"type": "expedite"})
	if err != nil {
		return err
	}
	_, err = c.stdin.Write(append(data, '\n'))
	return err
}

// SendApprovalSafe is a thread-safe version of SendApproval.
func (c *ChildProcess) SendApprovalSafe(approved bool, reason string) error {
	c.stdinMu.Lock()
	defer c.stdinMu.Unlock()
	return c.SendApproval(approved, reason)
}

// Wait waits for the child process to exit and returns its error (if any).
func (c *ChildProcess) Wait() error {
	return c.cmd.Wait()
}

// Kill terminates the child process and its entire process group, so any
// grandchildren (e.g. bash commands the subagent launched) are killed too
// rather than being orphaned and left running in the background.
func (c *ChildProcess) Kill() error {
	if c.cmd.Process == nil {
		return nil
	}
	// Negative PID targets the whole process group (set via Setpgid at spawn).
	if err := syscall.Kill(-c.cmd.Process.Pid, syscall.SIGKILL); err == nil {
		return nil
	}
	// Fall back to killing just the process if the group signal failed.
	return c.cmd.Process.Kill()
}

// Reap kills the child if still running and always waits to avoid zombies.
func (c *ChildProcess) Reap() {
	_ = c.Kill()
	_ = c.Wait()
}
