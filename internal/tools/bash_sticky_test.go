package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/testutil"
)

func bashOut(t *testing.T, res ToolResult) bashOutput {
	t.Helper()
	if res.Error != "" {
		t.Fatalf("bash error: %s", res.Error)
	}
	var out bashOutput
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatalf("unmarshal: %v\ncontent=%q", err, res.Content)
	}
	return out
}

func TestBashSticky_CwdPersistsAcrossCalls(t *testing.T) {
	dir := testutil.TempDir(t)
	sub := filepath.Join(dir, "nested")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	b := NewBashTool(dir, true, nil)

	res, _ := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command": "cd nested", "description": "enter nested",
	}))
	bashOut(t, res)
	got := b.StickyCwd()
	if got != sub {
		// Resolve symlinks (temp dirs on some OS).
		want, _ := filepath.EvalSymlinks(sub)
		got2, _ := filepath.EvalSymlinks(got)
		if got2 != want {
			t.Fatalf("sticky cwd = %q, want %q", got, sub)
		}
	}

	res, _ = b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command": "pwd", "description": "print cwd",
	}))
	out := bashOut(t, res)
	pwd := strings.TrimSpace(out.Stdout)
	want, _ := filepath.EvalSymlinks(sub)
	gotPwd, _ := filepath.EvalSymlinks(pwd)
	if gotPwd != want {
		t.Fatalf("pwd = %q, want sticky %q", pwd, sub)
	}
}

func TestBashSticky_EnvPersistsAcrossCalls(t *testing.T) {
	dir := testutil.TempDir(t)
	b := NewBashTool(dir, true, nil)

	res, _ := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command":     "export POISSON_STICKY_TEST=hello_sticky",
		"description": "set env",
	}))
	bashOut(t, res)

	res, _ = b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command":     "printf '%s' \"$POISSON_STICKY_TEST\"",
		"description": "read env",
	}))
	out := bashOut(t, res)
	if out.Stdout != "hello_sticky" {
		t.Fatalf("stdout = %q, want hello_sticky", out.Stdout)
	}
}

func TestBashSticky_WorkdirOverrideThenUpdates(t *testing.T) {
	dir := testutil.TempDir(t)
	a := filepath.Join(dir, "a")
	bdir := filepath.Join(dir, "b")
	os.Mkdir(a, 0o755)
	os.Mkdir(bdir, 0o755)
	tool := NewBashTool(dir, true, nil)

	res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command": "pwd", "description": "in a", "workdir": "a",
	}))
	out := bashOut(t, res)
	pwd, _ := filepath.EvalSymlinks(strings.TrimSpace(out.Stdout))
	want, _ := filepath.EvalSymlinks(a)
	if pwd != want {
		t.Fatalf("pwd = %q, want %q", pwd, a)
	}
	// Sticky should now be a.
	sticky, _ := filepath.EvalSymlinks(tool.StickyCwd())
	if sticky != want {
		t.Fatalf("sticky = %q, want %q", tool.StickyCwd(), a)
	}

	// Plain pwd (no workdir) still in a.
	res, _ = tool.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command": "pwd", "description": "still a",
	}))
	out = bashOut(t, res)
	pwd, _ = filepath.EvalSymlinks(strings.TrimSpace(out.Stdout))
	if pwd != want {
		t.Fatalf("pwd after sticky = %q, want %q", pwd, a)
	}
}

func TestBashSticky_FailedCommandStillUpdatesCwd(t *testing.T) {
	dir := testutil.TempDir(t)
	sub := filepath.Join(dir, "sub")
	os.Mkdir(sub, 0o755)
	b := NewBashTool(dir, true, nil)

	res, _ := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command":     "cd sub; false",
		"description": "cd then fail",
	}))
	out := bashOut(t, res)
	if out.ExitCode == 0 {
		t.Fatal("expected non-zero exit")
	}
	want, _ := filepath.EvalSymlinks(sub)
	got, _ := filepath.EvalSymlinks(b.StickyCwd())
	if got != want {
		t.Fatalf("sticky cwd = %q, want %q after failed command", b.StickyCwd(), sub)
	}
}

func TestBashSticky_SubagentIsolation(t *testing.T) {
	dir := testutil.TempDir(t)
	sub := filepath.Join(dir, "only-parent")
	os.Mkdir(sub, 0o755)
	parent := NewBashTool(dir, true, nil)
	child := NewBashTool(dir, true, nil) // separate instance = separate sticky

	bashOut(t, mustExec(t, parent, "cd only-parent", "parent cd"))
	// Child still at session root.
	res, _ := child.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command": "pwd", "description": "child pwd",
	}))
	out := bashOut(t, res)
	pwd, _ := filepath.EvalSymlinks(strings.TrimSpace(out.Stdout))
	root, _ := filepath.EvalSymlinks(dir)
	if pwd != root {
		t.Fatalf("child pwd = %q, want session root %q (not parent's sticky)", pwd, dir)
	}
	if child.StickyCwd() != "" && child.StickyCwd() != dir {
		// After first call sticky is set to root pwd absolute.
		c, _ := filepath.EvalSymlinks(child.StickyCwd())
		if c != root {
			t.Fatalf("child sticky leaked parent: %q", child.StickyCwd())
		}
	}
}

func mustExec(t *testing.T, b *BashTool, cmd, desc string) ToolResult {
	t.Helper()
	res, err := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command": cmd, "description": desc,
	}))
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestBashSticky_SerializesParallelCalls(t *testing.T) {
	dir := testutil.TempDir(t)
	for _, name := range []string{"d0", "d1", "d2", "d3", "d4"} {
		os.Mkdir(filepath.Join(dir, name), 0o755)
	}
	b := NewBashTool(dir, true, nil)

	var wg sync.WaitGroup
	errCh := make(chan string, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		i := i
		go func() {
			defer wg.Done()
			res, err := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
				"command":     "cd d" + string(rune('0'+i)) + " && pwd",
				"description": "race cd",
			}))
			if err != nil {
				errCh <- err.Error()
				return
			}
			if res.Error != "" {
				errCh <- res.Error
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for e := range errCh {
		t.Fatalf("parallel bash: %s", e)
	}
	// Sticky must be one of the dN dirs, not empty/corrupt.
	sc := b.StickyCwd()
	if sc == "" {
		t.Fatal("sticky cwd empty after parallel cds")
	}
	base := filepath.Base(sc)
	if len(base) != 2 || base[0] != 'd' {
		t.Fatalf("sticky cwd unexpected: %q", sc)
	}
}

// TestBashSticky_StaleCwdSelfHeals covers the "fork/exec /usr/bin/bash: no
// such file or directory" bug: if the directory a prior sticky `cd` landed
// in gets deleted before the next call, Execute must not hand a dead Dir to
// exec.Cmd (Go's own friendly chdir-existence check is disabled once
// SysProcAttr is set) — it should self-heal to the session root instead of
// wedging the session in a permanent exec failure.
func TestBashSticky_StaleCwdSelfHeals(t *testing.T) {
	dir := testutil.TempDir(t)
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	b := NewBashTool(dir, true, nil)

	bashOut(t, mustExec(t, b, "cd sub", "enter sub"))
	root, _ := filepath.EvalSymlinks(dir)
	sticky, _ := filepath.EvalSymlinks(b.StickyCwd())
	want, _ := filepath.EvalSymlinks(sub)
	if sticky != want {
		t.Fatalf("sticky cwd = %q, want %q", b.StickyCwd(), sub)
	}

	// The directory sticky cwd points at vanishes out from under it.
	if err := os.RemoveAll(sub); err != nil {
		t.Fatal(err)
	}

	res, err := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command": "pwd", "description": "pwd after dir vanished",
	}))
	if err != nil {
		t.Fatalf("Execute returned Go error (should self-heal, not crash): %v", err)
	}
	out := bashOut(t, res)
	if out.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0 (self-heal should still run the command)", out.ExitCode)
	}
	if !strings.Contains(out.Hint, "no longer exists") {
		t.Fatalf("hint = %q, want mention of the stale directory", out.Hint)
	}
	pwd, _ := filepath.EvalSymlinks(strings.TrimSpace(out.Stdout))
	if pwd != root {
		t.Fatalf("pwd = %q, want session root %q", pwd, root)
	}
	// Sticky cwd must be corrected too, or every later call repeats the bug.
	if sc, _ := filepath.EvalSymlinks(b.StickyCwd()); sc != root {
		t.Fatalf("sticky cwd after self-heal = %q, want session root %q", b.StickyCwd(), root)
	}
}

// TestBashSticky_BadWorkdirSelfHeals covers the same underlying bug for an
// explicit workdir param that doesn't exist (typo, deleted path).
func TestBashSticky_BadWorkdirSelfHeals(t *testing.T) {
	dir := testutil.TempDir(t)
	b := NewBashTool(dir, true, nil)

	res, err := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command": "pwd", "description": "bad workdir", "workdir": "does-not-exist",
	}))
	if err != nil {
		t.Fatalf("Execute returned Go error (should self-heal, not crash): %v", err)
	}
	out := bashOut(t, res)
	if out.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0", out.ExitCode)
	}
	if !strings.Contains(out.Hint, "does not exist") {
		t.Fatalf("hint = %q, want mention of the missing workdir", out.Hint)
	}
	root, _ := filepath.EvalSymlinks(dir)
	pwd, _ := filepath.EvalSymlinks(strings.TrimSpace(out.Stdout))
	if pwd != root {
		t.Fatalf("pwd = %q, want session root %q", pwd, root)
	}
}

// TestBashTool_ApprovalWaitDoesNotSerializeOtherCalls is the regression
// guard for narrowing the sticky mutex: it used to be held across the whole
// Execute (including the human-approval wait and the subprocess run), so a
// second bash call on the same BashTool instance couldn't even start while
// the first was blocked waiting for approval. The lock now only guards the
// brief cwd/env snapshot read/write.
func TestBashTool_ApprovalWaitDoesNotSerializeOtherCalls(t *testing.T) {
	dir := testutil.TempDir(t)
	release := make(chan struct{})
	started := make(chan struct{})
	b := NewBashTool(dir, false, func(ctx context.Context, cmd, desc, workdir string) (bool, string) {
		if strings.Contains(cmd, "slowmarker") {
			close(started)
			<-release
		}
		return true, ""
	})

	go b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command": "true # slowmarker", "description": "slow",
	}))
	<-started // slow call is now parked in its approval wait

	fastDone := make(chan struct{})
	go func() {
		b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
			"command": "echo fast", "description": "fast",
		}))
		close(fastDone)
	}()

	select {
	case <-fastDone:
		// good: the fast call ran to completion while the slow one is
		// still parked, proving the lock doesn't span the approval wait.
	case <-time.After(3 * time.Second):
		t.Fatal("second bash call blocked on the first's pending approval — sticky mutex over-scoped")
	}
	close(release)
}

func TestBashSticky_StdoutNotPollutedByDump(t *testing.T) {
	dir := testutil.TempDir(t)
	b := NewBashTool(dir, true, nil)
	res, _ := b.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"command": "echo only-this", "description": "echo",
	}))
	out := bashOut(t, res)
	if strings.TrimSpace(out.Stdout) != "only-this" {
		t.Fatalf("stdout polluted: %q", out.Stdout)
	}
}
