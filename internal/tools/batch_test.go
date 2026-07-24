package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/testutil"
)

func testBatchRegistry(t *testing.T, dir string) *Registry {
	t.Helper()
	return BuildRegistry(BuildOptions{Cwd: dir, Sandbox: true})
}

func TestBatch_MultipleReads(t *testing.T) {
	dir := testutil.TempDir(t)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("aaa\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("bbb\n"), 0o644)
	reg := testBatchRegistry(t, dir)

	res, err := reg.Execute(context.Background(), "batch", mustJSON(t, map[string]interface{}{
		"calls": []map[string]interface{}{
			{"tool": "read", "input": map[string]string{"path": "a.txt"}},
			{"tool": "read", "input": map[string]string{"path": "b.txt"}},
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != "" {
		t.Fatalf("batch error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "2 ok") {
		t.Errorf("header: %q", res.Content)
	}
	if !strings.Contains(res.Content, "1. read") || !strings.Contains(res.Content, "2. read") {
		t.Errorf("steps: %q", res.Content)
	}
	if !strings.Contains(res.Content, "aaa") || !strings.Contains(res.Content, "bbb") {
		t.Errorf("bodies: %q", res.Content)
	}
}

func TestBatch_EditsSerial(t *testing.T) {
	dir := testutil.TempDir(t)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("foo\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("foo\n"), 0o644)
	reg := testBatchRegistry(t, dir)

	res, _ := reg.Execute(context.Background(), "batch", mustJSON(t, map[string]interface{}{
		"calls": []map[string]interface{}{
			{"tool": "edit", "input": map[string]interface{}{"path": "a.txt", "oldText": "foo", "newText": "A"}},
			{"tool": "edit", "input": map[string]interface{}{"path": "b.txt", "oldText": "foo", "newText": "B"}},
		},
	}))
	if res.Error != "" {
		t.Fatalf("batch error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "2 ok") {
		t.Fatalf("content: %q", res.Content)
	}
	a, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	b, _ := os.ReadFile(filepath.Join(dir, "b.txt"))
	if string(a) != "A\n" || string(b) != "B\n" {
		t.Fatalf("a=%q b=%q", a, b)
	}
}

func TestBatch_PartialFailure(t *testing.T) {
	dir := testutil.TempDir(t)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("foo\n"), 0o644)
	reg := testBatchRegistry(t, dir)

	res, _ := reg.Execute(context.Background(), "batch", mustJSON(t, map[string]interface{}{
		"calls": []map[string]interface{}{
			{"tool": "edit", "input": map[string]interface{}{"path": "a.txt", "oldText": "foo", "newText": "ok"}},
			{"tool": "edit", "input": map[string]interface{}{"path": "missing.txt", "oldText": "x", "newText": "y"}},
		},
	}))
	if !strings.Contains(res.Content, "1 ok") || !strings.Contains(res.Content, "1 err") {
		t.Fatalf("content: %q", res.Content)
	}
	if !strings.Contains(res.Content, "2. edit — error:") {
		t.Fatalf("missing step error: %q", res.Content)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(got) != "ok\n" {
		t.Fatalf("first edit should have applied: %q", got)
	}
}

func TestBatch_DeniesBashAndSubagentAndNestedBatch(t *testing.T) {
	dir := testutil.TempDir(t)
	// Include SubApproval so subagent is registered — otherwise the deny
	// path is never hit (tool-not-registered wins first).
	reg := BuildRegistry(BuildOptions{
		Cwd:     dir,
		Sandbox: true,
		SubApproval: func(string, string, string, string, string) (bool, string) {
			return false, "no"
		},
	})
	if _, ok := reg.Get("subagent"); !ok {
		t.Fatal("subagent should be registered for this test")
	}

	for _, name := range []string{"bash", "subagent", "batch"} {
		res, _ := reg.Execute(context.Background(), "batch", mustJSON(t, map[string]interface{}{
			"calls": []map[string]interface{}{
				{"tool": name, "input": map[string]string{"x": "y"}},
			},
		}))
		if res.Error == "" {
			t.Fatalf("tool %q: expected top-level error, got content=%q", name, res.Content)
		}
		if !strings.Contains(res.Error, "not allowed") {
			t.Fatalf("tool %q: error = %q, want 'not allowed'", name, res.Error)
		}
	}
}

func TestBatch_TooManyCalls(t *testing.T) {
	dir := testutil.TempDir(t)
	reg := testBatchRegistry(t, dir)
	calls := make([]map[string]interface{}, batchMaxCalls+1)
	for i := range calls {
		calls[i] = map[string]interface{}{
			"tool":  "read",
			"input": map[string]string{"path": "nope"},
		}
	}
	res, _ := reg.Execute(context.Background(), "batch", mustJSON(t, map[string]interface{}{"calls": calls}))
	if res.Error == "" || !strings.Contains(res.Error, "too many") {
		t.Fatalf("error = %q", res.Error)
	}
}

func TestBatch_UnknownToolRejected(t *testing.T) {
	dir := testutil.TempDir(t)
	reg := testBatchRegistry(t, dir)
	res, _ := reg.Execute(context.Background(), "batch", mustJSON(t, map[string]interface{}{
		"calls": []map[string]interface{}{
			{"tool": "nope_tool", "input": map[string]string{}},
		},
	}))
	if res.Error == "" || !strings.Contains(res.Error, "not registered") {
		t.Fatalf("error = %q", res.Error)
	}
}

// A denied tool name mixed with valid calls must abort the whole batch
// before any step runs (validation), not partial-apply the good ones.
func TestBatch_DeniedToolAbortsBeforeAnyStep(t *testing.T) {
	dir := testutil.TempDir(t)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("foo\n"), 0o644)
	reg := testBatchRegistry(t, dir)

	res, _ := reg.Execute(context.Background(), "batch", mustJSON(t, map[string]interface{}{
		"calls": []map[string]interface{}{
			{"tool": "edit", "input": map[string]interface{}{"path": "a.txt", "oldText": "foo", "newText": "bar"}},
			{"tool": "bash", "input": map[string]string{"command": "true", "description": "noop"}},
		},
	}))
	if res.Error == "" || !strings.Contains(res.Error, "not allowed") {
		t.Fatalf("error = %q, want whole-batch not allowed", res.Error)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(got) != "foo\n" {
		t.Fatalf("edit must not have run; file = %q", got)
	}
}
