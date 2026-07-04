package subagent

import (
	"bufio"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// processAlive reports whether a PID is still a live (non-reaped) process.
func processAlive(pid int) bool {
	// Signal 0 performs error checking without sending a signal.
	err := syscall.Kill(pid, 0)
	return err == nil
}

// waitDead polls until the PID is gone or the timeout elapses.
func waitDead(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return !processAlive(pid)
}

// TestReapKillsProcessGroup verifies that Reap()/Kill() tears down the entire
// process group — the child and any grandchildren it spawned — so a Ctrl+C
// cancel never leaves subagent subprocesses running in the background.
func TestReapKillsProcessGroup(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	// The shell spawns a long-lived background grandchild, prints its PID, then
	// blocks. Both live in the shell's process group (no job control).
	cmd := exec.Command("sh", "-c", "sleep 300 & echo $!; sleep 300")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	c := &ChildProcess{cmd: cmd, stdout: bufio.NewReader(stdout)}
	childPID := cmd.Process.Pid

	line, err := c.stdout.ReadString('\n')
	if err != nil {
		c.Reap()
		t.Fatalf("read grandchild pid: %v", err)
	}
	grandPID, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		c.Reap()
		t.Fatalf("parse grandchild pid %q: %v", line, err)
	}

	if !processAlive(grandPID) {
		t.Fatalf("grandchild %d should be running before Reap", grandPID)
	}

	// Simulate the Ctrl+C path: the subagent tool reaps the child on cancel.
	c.Reap()

	if !waitDead(grandPID, 2*time.Second) {
		t.Errorf("grandchild %d survived Reap — orphaned background process", grandPID)
	}
	if !waitDead(childPID, 2*time.Second) {
		t.Errorf("child %d survived Reap", childPID)
	}
}

// TestReapNilProcessSafe verifies Reap is safe on a never-started child.
func TestReapNilProcessSafe(t *testing.T) {
	c := &ChildProcess{cmd: exec.Command("true")}
	c.Reap() // must not panic
}
