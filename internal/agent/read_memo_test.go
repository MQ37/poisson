package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/config"
	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/store"
	"github.com/mq37/poisson/internal/testutil"
	"github.com/mq37/poisson/internal/tools"
)

// newRWAgent is like newCtxAgent but also registers edit/write, for tests
// that need to invalidate a memoized read.
func newRWAgent(t *testing.T, cwd string, st *store.Store, sid string, resp [][]provider.StreamEvent) *Agent {
	t.Helper()
	cfg := config.DefaultConfig()
	if err := st.CreateSession(&store.Session{
		ID: sid, Cwd: cwd, Provider: "ollama", Model: cfg.Ollama.Model,
		CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	prov := provider.NewFakeProvider("ollama", []provider.Model{{ID: cfg.Ollama.Model, ContextWindow: 8192}})
	prov.SetResponses(resp)
	reg := tools.NewRegistry()
	reg.Register(tools.NewReadTool(cwd, alwaysApprove))
	reg.Register(tools.NewEditTool(cwd, alwaysApprove))
	reg.Register(tools.NewWriteTool(cwd, alwaysApprove))
	a := NewAgent(st, prov, reg, cfg, sid, make(chan OutputEvent, 256), func(context.Context, string, string, string) (bool, string) { return true, "" })
	a.SetModel(cfg.Ollama.Model)
	return a
}

// lastToolResult returns the content of the most recently stored "tool" message.
func lastToolResult(t *testing.T, st *store.Store, sid string) string {
	t.Helper()
	msgs, err := st.GetMessages(sid)
	if err != nil {
		t.Fatal(err)
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "tool" {
			return msgs[i].Content
		}
	}
	t.Fatal("no tool message found")
	return ""
}

// TestReadMemoizationSkipsUnchangedReread verifies a second read of the same
// path+range, with the file untouched in between, is answered with a short
// stub instead of the file content again.
func TestReadMemoizationSkipsUnchangedReread(t *testing.T) {
	testutil.TempHome(t)
	root := testutil.TempDir(t)
	writeFile(t, filepath.Join(root, "f.go"), "line one\nline two\nline three\n")

	st, err := store.Open(filepath.Join(root, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sid := "memo-sess"

	r1a, r1b := provider.FakeToolCallResponse("read", map[string]interface{}{"path": "f.go"}, "done")
	r2a, r2b := provider.FakeToolCallResponse("read", map[string]interface{}{"path": "f.go"}, "done")
	a := newRWAgent(t, root, st, sid, [][]provider.StreamEvent{r1a, r1b, r2a, r2b})
	stop := drain(a)
	defer close(stop)

	if err := a.PromptWithContext(context.Background(), "read f.go"); err != nil {
		t.Fatalf("prompt 1: %v", err)
	}
	first := lastToolResult(t, st, sid)
	if !strings.Contains(first, "line one") {
		t.Fatalf("first read should contain file content, got: %s", first)
	}

	if err := a.PromptWithContext(context.Background(), "read f.go again"); err != nil {
		t.Fatalf("prompt 2: %v", err)
	}
	second := lastToolResult(t, st, sid)
	if strings.Contains(second, "line one") {
		t.Errorf("second read should be memoized (no file content), got: %s", second)
	}
	if !strings.Contains(second, "unchanged") {
		t.Errorf("second read should say unchanged, got: %s", second)
	}
}

// TestReadMemoizationInvalidatedByEdit verifies a read that follows an edit
// of the same file is NOT memoized — the model must see the new content.
func TestReadMemoizationInvalidatedByEdit(t *testing.T) {
	testutil.TempHome(t)
	root := testutil.TempDir(t)
	path := filepath.Join(root, "f.go")
	writeFile(t, path, "old content\n")

	st, err := store.Open(filepath.Join(root, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sid := "memo-edit-sess"

	r1a, r1b := provider.FakeToolCallResponse("read", map[string]interface{}{"path": "f.go"}, "done")
	r2a, r2b := provider.FakeToolCallResponse("edit", map[string]interface{}{
		"path": "f.go", "oldText": "old content\n", "newText": "new content\n",
	}, "edited")
	r3a, r3b := provider.FakeToolCallResponse("read", map[string]interface{}{"path": "f.go"}, "done")
	a := newRWAgent(t, root, st, sid, [][]provider.StreamEvent{r1a, r1b, r2a, r2b, r3a, r3b})
	stop := drain(a)
	defer close(stop)

	if err := a.PromptWithContext(context.Background(), "read f.go"); err != nil {
		t.Fatalf("prompt 1: %v", err)
	}
	if err := a.PromptWithContext(context.Background(), "edit f.go"); err != nil {
		t.Fatalf("prompt 2: %v", err)
	}
	if err := a.PromptWithContext(context.Background(), "read f.go again"); err != nil {
		t.Fatalf("prompt 3: %v", err)
	}
	third := lastToolResult(t, st, sid)
	if strings.Contains(third, "unchanged") {
		t.Errorf("read after edit must not be memoized, got: %s", third)
	}
	if !strings.Contains(third, "new content") {
		t.Errorf("read after edit should show new content, got: %s", third)
	}
}

// TestReadMemoizationNarrowerRangeCovered verifies a request for a subset of
// an already-memoized full read is itself memoized.
func TestReadMemoizationNarrowerRangeCovered(t *testing.T) {
	testutil.TempHome(t)
	root := testutil.TempDir(t)
	writeFile(t, filepath.Join(root, "f.go"), "one\ntwo\nthree\nfour\nfive\n")

	st, err := store.Open(filepath.Join(root, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sid := "memo-range-sess"

	r1a, r1b := provider.FakeToolCallResponse("read", map[string]interface{}{"path": "f.go"}, "done")
	r2a, r2b := provider.FakeToolCallResponse("read", map[string]interface{}{
		"path": "f.go", "offset": 2, "limit": 2,
	}, "done")
	a := newRWAgent(t, root, st, sid, [][]provider.StreamEvent{r1a, r1b, r2a, r2b})
	stop := drain(a)
	defer close(stop)

	if err := a.PromptWithContext(context.Background(), "read f.go"); err != nil {
		t.Fatalf("prompt 1: %v", err)
	}
	if err := a.PromptWithContext(context.Background(), "read lines 2-3 of f.go"); err != nil {
		t.Fatalf("prompt 2: %v", err)
	}
	second := lastToolResult(t, st, sid)
	if !strings.Contains(second, "unchanged") {
		t.Errorf("narrower re-read should be memoized, got: %s", second)
	}
}

// TestReadMemoizationRangeShapedOffset verifies memoization still sees the
// real line range when a call uses the lenient range-shaped offset the read
// tool accepts ("2, 3" in the single offset field) — the memo layer must
// parse it exactly like the tool does (tools.ParseReadCall), not skip it.
func TestReadMemoizationRangeShapedOffset(t *testing.T) {
	testutil.TempHome(t)
	root := testutil.TempDir(t)
	writeFile(t, filepath.Join(root, "f.go"), "one\ntwo\nthree\nfour\nfive\n")

	st, err := store.Open(filepath.Join(root, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sid := "memo-rangeoffset-sess"

	r1a, r1b := provider.FakeToolCallResponse("read", map[string]interface{}{"path": "f.go"}, "done")
	r2a, r2b := provider.FakeToolCallResponse("read", map[string]interface{}{
		"path": "f.go", "offset": "2, 3",
	}, "done")
	a := newRWAgent(t, root, st, sid, [][]provider.StreamEvent{r1a, r1b, r2a, r2b})
	stop := drain(a)
	defer close(stop)

	if err := a.PromptWithContext(context.Background(), "read f.go"); err != nil {
		t.Fatalf("prompt 1: %v", err)
	}
	if err := a.PromptWithContext(context.Background(), "read lines 2-3 of f.go"); err != nil {
		t.Fatalf("prompt 2: %v", err)
	}
	second := lastToolResult(t, st, sid)
	if !strings.Contains(second, "unchanged") {
		t.Errorf("range-shaped offset re-read should be memoized, got: %s", second)
	}
}

// TestReadMemoizationWiderRangeNotCovered verifies a request for a WIDER
// range than a previously memoized narrow read does a real read.
func TestReadMemoizationWiderRangeNotCovered(t *testing.T) {
	testutil.TempHome(t)
	root := testutil.TempDir(t)
	writeFile(t, filepath.Join(root, "f.go"), "one\ntwo\nthree\nfour\nfive\n")

	st, err := store.Open(filepath.Join(root, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sid := "memo-wide-sess"

	r1a, r1b := provider.FakeToolCallResponse("read", map[string]interface{}{
		"path": "f.go", "offset": 2, "limit": 1,
	}, "done")
	r2a, r2b := provider.FakeToolCallResponse("read", map[string]interface{}{"path": "f.go"}, "done")
	a := newRWAgent(t, root, st, sid, [][]provider.StreamEvent{r1a, r1b, r2a, r2b})
	stop := drain(a)
	defer close(stop)

	if err := a.PromptWithContext(context.Background(), "read line 2 of f.go"); err != nil {
		t.Fatalf("prompt 1: %v", err)
	}
	if err := a.PromptWithContext(context.Background(), "read all of f.go"); err != nil {
		t.Fatalf("prompt 2: %v", err)
	}
	second := lastToolResult(t, st, sid)
	if strings.Contains(second, "unchanged") {
		t.Errorf("wider re-read must not be memoized, got: %s", second)
	}
	if !strings.Contains(second, "one") || !strings.Contains(second, "five") {
		t.Errorf("wider re-read should show the whole file, got: %s", second)
	}
}

// TestReadMemoizationResetsOnSessionSwitch verifies SwitchSession clears
// memoized reads (resetContextTracker), matching loadedContextDirs semantics.
func TestReadMemoizationResetsOnSessionSwitch(t *testing.T) {
	testutil.TempHome(t)
	root := testutil.TempDir(t)
	writeFile(t, filepath.Join(root, "f.go"), "hello\n")

	st, err := store.Open(filepath.Join(root, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sid := "memo-switch-sess"

	r1a, r1b := provider.FakeToolCallResponse("read", map[string]interface{}{"path": "f.go"}, "done")
	a := newRWAgent(t, root, st, sid, [][]provider.StreamEvent{r1a, r1b})
	stop := drain(a)
	defer close(stop)

	if err := a.PromptWithContext(context.Background(), "read f.go"); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	a.contextMu.Lock()
	n := len(a.readMemos)
	a.contextMu.Unlock()
	if n == 0 {
		t.Fatal("expected a memoized read after a successful read")
	}

	a.SwitchSession(sid)
	a.contextMu.Lock()
	n = len(a.readMemos)
	a.contextMu.Unlock()
	if n != 0 {
		t.Errorf("SwitchSession should clear readMemos, got %d entries", n)
	}
}

// TestRangeCovers is a table-driven unit test of the pure coverage-comparison
// helper, independent of the filesystem/agent machinery above.
func TestRangeCovers(t *testing.T) {
	cases := []struct {
		name                  string
		haveOffset, haveLimit int
		wantOffset, wantLimit int
		want                  bool
	}{
		{"identical unbounded", 0, 0, 0, 0, true},
		{"identical bounded", 1, 100, 1, 100, true},
		{"subset within bounded", 1, 100, 10, 20, true},
		{"subset starts at same line", 5, 50, 5, 10, true},
		{"exceeds bounded end", 1, 10, 5, 10, false},
		{"starts before memoized", 5, 10, 1, 5, false},
		{"unbounded request vs bounded memo", 1, 10, 1, 0, false},
		{"bounded request vs unbounded memo", 0, 0, 100, 5, true},
		{"offset zero means line one", 0, 10, 1, 5, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := rangeCovers(c.haveOffset, c.haveLimit, c.wantOffset, c.wantLimit)
			if got != c.want {
				t.Errorf("rangeCovers(%d,%d,%d,%d) = %v, want %v",
					c.haveOffset, c.haveLimit, c.wantOffset, c.wantLimit, got, c.want)
			}
		})
	}
}
