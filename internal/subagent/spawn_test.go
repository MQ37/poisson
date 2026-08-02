package subagent

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// processAlive reports whether a PID is still running (not exited AND not a
// zombie). Signal-0 liveness (kill(pid, 0) == nil) alone isn't enough in a
// container with no real init/reaper (this sandbox's PID 1 is literally
// `sleep infinity`, confirmed by prior scouting): a SIGKILLed process still
// occupies its PID-table slot as a <defunct> zombie until *something* calls
// wait() on it, which never happens here — kill(pid, 0) keeps succeeding
// forever even though the process is stopped and consumes no resources.
// Reading /proc/[pid]/stat's state field distinguishes "actually still
// running" from "killed, just unreaped" so Kill()/Reap() tests measure what
// they're actually supposed to guarantee (the target stops running) rather
// than something this environment's broken PID 1 makes untestable.
func processAlive(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err == nil {
		if idx := strings.LastIndexByte(string(data), ')'); idx >= 0 && idx+2 < len(data) {
			if fields := strings.Fields(string(data)[idx+2:]); len(fields) > 0 {
				return fields[0] != "Z"
			}
		}
	}
	// /proc unavailable (non-Linux) or unparsable: fall back to signal-0.
	return syscall.Kill(pid, 0) == nil
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

// TestSendExpediteWritesExactExpediteShape is the end-to-end guard for
// SendExpedite, the one leg of the expedite fan-out chain
// (ChildProcess.SendExpedite -> tools.SubagentTool.ExpediteAll ->
// agent.Agent.ExpediteSubagents) that had zero test at any layer. Mirrors
// TestSpawnEndToEndWithFakeChildProcess's real pipe-based fake-child-process
// pattern (spawn_args_test.go): a real spawned "child" via
// SetLookupExecutableForTest, real stdin/stdout pipes, zero real LLM calls.
// Instead of grepping the received line for a substring (as the existing
// approval round-trip test does), this captures the raw bytes that actually
// crossed the pipe to a file and compares them byte-for-byte — proving the
// wire shape is exactly `{"type":"expedite"}`, not accidentally
// SendApproval's shape (which additionally carries "approved"/"reason").
func TestSendExpediteWritesExactExpediteShape(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	scriptPath := dir + "/fake-child-expedite.sh"
	capturePath := dir + "/captured-stdin-line.json"
	// Reads exactly one line off stdin and dumps it verbatim (no reformatting,
	// no substring test) to CAPTURE_FILE, so the Go side can assert on the
	// exact bytes SendExpedite put on the wire.
	script := `#!/bin/sh
read -r line
printf '%s' "$line" > "$CAPTURE_FILE"
printf '{"type":"done","success":true}\n'
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake child script: %v", err)
	}
	restore := SetLookupExecutableForTest(scriptPath)
	defer restore()

	child, err := Spawn(SpawnInput{
		Task:      "do something",
		Cwd:       ".",
		SessionID: "sess-expedite",
		ExtraEnv:  []string{"CAPTURE_FILE=" + capturePath},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer child.Reap()

	if err := child.SendExpedite(); err != nil {
		t.Fatalf("SendExpedite: %v", err)
	}

	ev, err := child.ReadEvent()
	if err != nil || ev == nil || ev.Type != "done" {
		t.Fatalf("ReadEvent (done) = %+v, err=%v", ev, err)
	}

	data, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("read captured stdin line: %v", err)
	}
	got := string(data)
	const want = `{"type":"expedite"}`
	if got != want {
		t.Errorf("child's stdin received %q via SendExpedite, want exactly %q (not the SendApproval shape, which additionally carries approved/reason fields)", got, want)
	}
}

// TestKillReachesGrandchildInSeparateProcessGroup reproduces the real
// subagent scenario: internal/tools/bash.go runs every command it execs in
// its OWN new process group (Setpgid:true), distinct from the subagent
// process's own group. Before killDescendantTree, ChildProcess.Kill()'s
// pgid-targeted SIGKILL never reached that separate group, orphaning
// whatever the subagent was running via bash.go at the moment it got
// killed from the outside (Ctrl+C, subagent timeout, parent cancellation).
func TestKillReachesGrandchildInSeparateProcessGroup(t *testing.T) {
	if _, err := os.Stat("/proc"); err != nil {
		t.Skip("/proc not available on this OS")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid not available")
	}

	// The outer shell (the "subagent") backgrounds `setsid sleep 300` — a
	// real, portable way (no manual syscall.Setpgid from an unrelated
	// process, which POSIX only permits from the direct parent, before the
	// target has exec'd) to get a grandchild in its OWN new session/process
	// group, exactly like bash.go's Setpgid:true does for every command a
	// subagent runs.
	cmd := exec.Command("sh", "-c", "setsid sleep 300 & echo $!; wait")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	c := &ChildProcess{cmd: cmd, stdout: bufio.NewReader(stdout)}

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

	// $! is the PID of the forked-but-not-yet-exec'd shell job; `setsid`
	// (and, after it, `sleep`) only becomes its own process-group/session
	// leader once that fork actually execs into setsid — a race with
	// however long that takes. Poll until pgid == grandPID itself (setsid's
	// whole point) before proceeding, so the kill below is guaranteed to
	// race against an already-separated group, not an in-flight fork.
	if !waitOwnProcessGroup(grandPID, 2*time.Second) {
		t.Fatalf("grandchild %d never became its own process group leader (setsid didn't run in time)", grandPID)
	}

	c.Reap()

	if !waitDead(grandPID, 2*time.Second) {
		t.Errorf("grandchild %d in a separate process group survived Kill — orphaned subprocess (the bug killDescendantTree fixes)", grandPID)
	}
}

// waitOwnProcessGroup polls /proc/[pid]/stat until pid is its own process
// group leader (field 5, pgrp, equals pid) or the timeout elapses.
func waitOwnProcessGroup(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pgid, ok := statPgid(pid); ok && pgid == pid {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	pgid, ok := statPgid(pid)
	return ok && pgid == pid
}

// statPgid reads the pgrp field (5th field, right after "pid (comm) state")
// from /proc/[pid]/stat.
func statPgid(pid int) (int, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, false
	}
	idx := strings.LastIndexByte(string(data), ')')
	if idx < 0 || idx+2 >= len(data) {
		return 0, false
	}
	fields := strings.Fields(string(data)[idx+2:])
	// fields[0]=state, fields[1]=ppid, fields[2]=pgrp.
	if len(fields) < 3 {
		return 0, false
	}
	pgid, err := strconv.Atoi(fields[2])
	if err != nil {
		return 0, false
	}
	return pgid, true
}
