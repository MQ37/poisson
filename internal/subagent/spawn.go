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
	Sandbox   bool
	ExtraEnv  []string
	DBPath    string // ephemeral DB path for the child (empty = parent's DB)
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
}

// Spawn starts a child Poisson process in JSON output mode.
func Spawn(input SpawnInput) (*ChildProcess, error) {
	// The child is flagged via POISSON_SUBAGENT_CHILD below; runChildMode then
	// builds the registry with Child:true, which grants every tool except
	// subagent (so subagents cannot spawn subagents).
	args := []string{
		"--json",
		"--no-skills",
		"--session", input.SessionID,
	}
	if input.Task != "" {
		args = append(args, "--", input.Task)
	}

	bin, err := os.Executable()
	if err != nil {
		bin = "px"
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = input.Cwd

	// Environment: inherit parent env so PATH/HOME/etc. are available.
	env := append(os.Environ(), input.ExtraEnv...)
	env = append(env, "POISSON_SUBAGENT_CHILD=1")
	if input.Provider != "" {
		env = append(env, fmt.Sprintf("POISSON_SUBAGENT_PROVIDER=%s", input.Provider))
	}
	if input.Model != "" {
		env = append(env, fmt.Sprintf("POISSON_SUBAGENT_MODEL=%s", input.Model))
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
	if input.Sandbox {
		env = append(env, "POISSON_SANDBOX=1")
	}
	cmd.Env = env

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
