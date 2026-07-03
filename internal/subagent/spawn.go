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
)

// SpawnInput holds the parameters for spawning a child Poisson process.
type SpawnInput struct {
	Task       string
	Cwd        string
	SessionID  string
	Name       string
	Provider   string
	Model      string
	Effort     string
	Sandbox    bool
	ExtraEnv   []string
	ChildTools []string // tool names available to the child
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
	Error         string          `json:"error,omitempty"`
}

// Spawn starts a child Poisson process in JSON output mode.
func Spawn(input SpawnInput) (*ChildProcess, error) {
	if input.ChildTools == nil {
		input.ChildTools = []string{"read", "write", "edit", "bash", "search", "ls", "glob"}
	}

	args := []string{
		"--json",
		"--no-skills",
		"--session", input.SessionID,
		"--tools", strings.Join(input.ChildTools, ","),
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
	if input.Sandbox {
		env = append(env, "POISSON_SANDBOX=1")
	}
	cmd.Env = env

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

// SendApproval writes an approval response to the child's stdin.
func (c *ChildProcess) SendApproval(approved bool) error {
	data, err := json.Marshal(map[string]interface{}{
		"type":     "approval_response",
		"approved": approved,
	})
	if err != nil {
		return err
	}
	_, err = c.stdin.Write(append(data, '\n'))
	return err
}

// SendApprovalSafe is a thread-safe version of SendApproval.
func (c *ChildProcess) SendApprovalSafe(approved bool) error {
	c.stdinMu.Lock()
	defer c.stdinMu.Unlock()
	return c.SendApproval(approved)
}

// Wait waits for the child process to exit and returns its error (if any).
func (c *ChildProcess) Wait() error {
	return c.cmd.Wait()
}

// Kill terminates the child process.
func (c *ChildProcess) Kill() error {
	if c.cmd.Process == nil {
		return nil
	}
	return c.cmd.Process.Kill()
}

// Reap kills the child if still running and always waits to avoid zombies.
func (c *ChildProcess) Reap() {
	_ = c.Kill()
	_ = c.Wait()
}