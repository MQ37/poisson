package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/mq37/poisson/internal/testutil"
)

func testBatchRegistry(t *testing.T, dir string) *Registry {
	t.Helper()
	return BuildRegistry(BuildOptions{Cwd: dir, ApprovalFn: alwaysApprove, FileApprovalFn: alwaysApprove})
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

func TestBatch_DeniesNestedBatch(t *testing.T) {
	dir := testutil.TempDir(t)
	reg := BuildRegistry(BuildOptions{Cwd: dir, ApprovalFn: alwaysApprove, FileApprovalFn: alwaysApprove})

	res, _ := reg.Execute(context.Background(), "batch", mustJSON(t, map[string]interface{}{
		"calls": []map[string]interface{}{
			{"tool": "batch", "input": map[string]string{"x": "y"}},
		},
	}))
	if res.Error == "" {
		t.Fatalf("expected top-level error, got content=%q", res.Content)
	}
	if !strings.Contains(res.Error, "not allowed") {
		t.Fatalf("error = %q, want 'not allowed'", res.Error)
	}
}

// TestBatch_AllowsBash verifies bash may run inside batch, gated by the
// same ApprovalFn as a direct call, and its real output reaches the batch
// step body.
func TestBatch_AllowsBash(t *testing.T) {
	dir := testutil.TempDir(t)
	reg := BuildRegistry(BuildOptions{Cwd: dir, ApprovalFn: alwaysApprove, FileApprovalFn: alwaysApprove})

	res, _ := reg.Execute(context.Background(), "batch", mustJSON(t, map[string]interface{}{
		"calls": []map[string]interface{}{
			{"tool": "bash", "input": map[string]string{"command": "echo from_batch", "description": "echo"}},
		},
	}))
	if res.Error != "" {
		t.Fatalf("batch error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "1 ok") || !strings.Contains(res.Content, "from_batch") {
		t.Fatalf("content: %q", res.Content)
	}
}

// TestBatch_BashApprovalGateStillApplies verifies dispatching bash through
// batch does not bypass ApprovalFn — a denied command still surfaces as a
// per-step error, same as calling bash directly.
func TestBatch_BashApprovalGateStillApplies(t *testing.T) {
	dir := testutil.TempDir(t)
	reg := BuildRegistry(BuildOptions{
		Cwd: dir,
		ApprovalFn: func(context.Context, string, string, string) (bool, string) {
			return false, "denied by test"
		},
	})

	res, _ := reg.Execute(context.Background(), "batch", mustJSON(t, map[string]interface{}{
		"calls": []map[string]interface{}{
			{"tool": "bash", "input": map[string]string{"command": "echo nope", "description": "echo"}},
		},
	}))
	if !strings.Contains(res.Content, "1 err") || !strings.Contains(res.Content, "denied by test") {
		t.Fatalf("expected approval denial to surface through batch, got: %q", res.Content)
	}
}

// TestBatch_AllowsSubagentAtTopLevel verifies subagent may be dispatched
// through batch when the registry has it (parent session) — batch's own
// tool allowlist doesn't reject it. It still fails here because the test
// registry has no runtime resolver wired (BindSubagentRuntime), same as
// calling the subagent tool directly outside batch.
func TestBatch_AllowsSubagentAtTopLevel(t *testing.T) {
	dir := testutil.TempDir(t)
	reg := BuildRegistry(BuildOptions{
		Cwd:        dir,
		ApprovalFn: alwaysApprove, FileApprovalFn: alwaysApprove,
		SubApproval: func(string, string, string, string, string) (bool, string) {
			return true, ""
		},
	})

	res, _ := reg.Execute(context.Background(), "batch", mustJSON(t, map[string]interface{}{
		"calls": []map[string]interface{}{
			{"tool": "subagent", "input": map[string]string{"task": "do something"}},
		},
	}))
	if strings.Contains(res.Error, "not allowed") {
		t.Fatalf("subagent should be allowed inside batch, got: %q", res.Error)
	}
	if !strings.Contains(res.Content, "1. subagent — error:") || !strings.Contains(res.Content, "runtime not configured") {
		t.Fatalf("expected subagent step to reach the subagent tool itself, got: %q", res.Content)
	}
}

// TestBatch_SubagentNotRegisteredInChild verifies a subagent's own registry
// (BuildOptions.Child) never has the subagent tool, so dispatching it
// through batch fails the same way a direct call would — recursion stays
// bounded to one level regardless of the batch path.
func TestBatch_SubagentNotRegisteredInChild(t *testing.T) {
	dir := testutil.TempDir(t)
	reg := BuildRegistry(BuildOptions{
		Cwd:        dir,
		ApprovalFn: alwaysApprove, FileApprovalFn: alwaysApprove,
		Child: true,
		SubApproval: func(string, string, string, string, string) (bool, string) {
			return true, ""
		},
	})
	if _, ok := reg.Get("subagent"); ok {
		t.Fatal("child registry must not have the subagent tool registered")
	}

	res, _ := reg.Execute(context.Background(), "batch", mustJSON(t, map[string]interface{}{
		"calls": []map[string]interface{}{
			{"tool": "subagent", "input": map[string]string{"task": "do something"}},
		},
	}))
	if !strings.Contains(res.Error, "not registered") {
		t.Fatalf("expected 'not registered' error, got: error=%q content=%q", res.Error, res.Content)
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
			{"tool": "batch", "input": map[string]string{"calls": "nope"}},
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

func TestBatchCallID(t *testing.T) {
	if got := BatchCallID("outer-1", 3); got != "outer-1.3" {
		t.Fatalf("BatchCallID = %q, want %q", got, "outer-1.3")
	}
}

func TestParseBatchCalls(t *testing.T) {
	input := mustJSON(t, map[string]interface{}{
		"calls": []map[string]interface{}{
			{"tool": "subagent", "input": map[string]string{"task": "x"}},
			{"tool": "read", "input": map[string]string{"path": "a.go"}},
		},
	})
	specs := ParseBatchCalls(input)
	if len(specs) != 2 || specs[0].Tool != "subagent" || specs[1].Tool != "read" {
		t.Fatalf("specs = %+v", specs)
	}
}

func TestParseBatchCalls_InvalidInputReturnsNil(t *testing.T) {
	if specs := ParseBatchCalls([]byte("not json")); specs != nil {
		t.Fatalf("specs = %+v, want nil", specs)
	}
}

// TestBatch_ThreadsSyntheticToolCallIDForSubagent is the regression guard for
// giving a batched subagent call its own live TUI widget: without a distinct
// per-nested-call ID in context, SubagentTool's existing progress-reporting
// (which reads its ID from ctx) would report every nested subagent under the
// same outer batch call ID, conflating their widgets.
func TestBatch_ThreadsSyntheticToolCallIDForSubagent(t *testing.T) {
	reg := NewRegistry()
	var mu sync.Mutex
	var gotIDs []string
	reg.Register(staticToolWithCtx{name: "subagent", onExecute: func(ctx context.Context) {
		id, _ := ToolCallIDFromContext(ctx)
		mu.Lock()
		gotIDs = append(gotIDs, id)
		mu.Unlock()
	}})
	bt := NewBatchTool(reg)
	reg.Register(bt)

	ctx := WithToolCallID(context.Background(), "outer-1")
	_, err := bt.Execute(ctx, mustJSON(t, map[string]interface{}{
		"calls": []map[string]interface{}{
			{"tool": "subagent", "input": map[string]string{"task": "a"}},
			{"tool": "subagent", "input": map[string]string{"task": "b"}},
		},
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	sort.Strings(gotIDs)
	want := []string{"outer-1.0", "outer-1.1"}
	if !reflect.DeepEqual(gotIDs, want) {
		t.Fatalf("ctx tool-call-ids = %v, want %v", gotIDs, want)
	}
}

// TestBatch_NotifiesSubagentDoneFn is the regression guard for flipping a
// batched subagent's widget to done the moment ITS call finishes, not only
// when the whole batch does.
func TestBatch_NotifiesSubagentDoneFn(t *testing.T) {
	reg := NewRegistry()
	reg.Register(staticTool{name: "subagent", result: ToolResult{Content: "child done"}})
	reg.Register(staticTool{name: "read", result: ToolResult{Content: "file text"}})
	bt := NewBatchTool(reg)
	reg.Register(bt)

	var gotID string
	var gotRes ToolResult
	calls := 0
	bt.SetSubagentDoneFn(func(toolCallID string, res ToolResult) {
		calls++
		gotID, gotRes = toolCallID, res
	})

	ctx := WithToolCallID(context.Background(), "outer-2")
	_, err := bt.Execute(ctx, mustJSON(t, map[string]interface{}{
		"calls": []map[string]interface{}{
			{"tool": "read", "input": map[string]string{"path": "a.go"}},
			{"tool": "subagent", "input": map[string]string{"task": "a"}},
		},
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if calls != 1 {
		t.Fatalf("subagentDoneFn called %d times, want exactly 1 (not for the read call)", calls)
	}
	if gotID != "outer-2.1" {
		t.Fatalf("toolCallID = %q, want %q", gotID, "outer-2.1")
	}
	if gotRes.Content != "child done" {
		t.Fatalf("result content = %q, want %q", gotRes.Content, "child done")
	}
}

// staticToolWithCtx is like staticTool but also observes the context passed
// to Execute — used to prove BatchTool threads a distinct ToolCallID per
// nested call.
type staticToolWithCtx struct {
	name      string
	onExecute func(ctx context.Context)
}

func (t staticToolWithCtx) Name() string            { return t.name }
func (t staticToolWithCtx) Description() string     { return "static test tool" }
func (t staticToolWithCtx) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t staticToolWithCtx) Execute(ctx context.Context, _ json.RawMessage) (ToolResult, error) {
	t.onExecute(ctx)
	return ToolResult{Content: "ok"}, nil
}
