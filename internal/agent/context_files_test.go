package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/config"
	"github.com/mq37/poisson/internal/project"
	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/store"
	"github.com/mq37/poisson/internal/testutil"
	"github.com/mq37/poisson/internal/tools"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// storedToolResults concatenates the content of every stored "tool" message.
func storedToolResults(t *testing.T, st *store.Store, sid string) string {
	t.Helper()
	msgs, err := st.GetMessages(sid)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, m := range msgs {
		if m.Role == "tool" {
			b.WriteString(m.Content)
		}
	}
	return b.String()
}

func newCtxAgent(t *testing.T, cwd string, st *store.Store, sid string, resp [][]provider.StreamEvent) *Agent {
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
	reg.Register(tools.NewReadTool(cwd, true, nil))
	a := NewAgent(st, prov, reg, cfg, sid, make(chan OutputEvent, 256), func(context.Context, string, string, string) (bool, string) { return true, "" })
	a.SetModel(cfg.Ollama.Model)
	return a
}

// drain empties the agent's output channel so the turn goroutine never blocks.
func drain(a *Agent) chan struct{} {
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case <-a.outputChan:
			}
		}
	}()
	return done
}

// TestContextInjectionOnFileWork verifies AGENTS.md for a worked-on file's dir
// (and the cwd->dir chain) is injected into the tool result once, that the
// cwd's own file is not re-injected, and that compaction resets the tracker.
func TestContextInjectionOnFileWork(t *testing.T) {
	testutil.TempHome(t)
	root := testutil.TempDir(t)
	sub := filepath.Join(root, "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "AGENTS.md"), "ROOT-RULES")      // cwd -> system prompt, never injected
	writeFile(t, filepath.Join(sub, "AGENTS.md"), "PKG-RULES-UNIQUE") // should be injected
	writeFile(t, filepath.Join(sub, "x.go"), "package pkg")           // the file we read
	writeFile(t, filepath.Join(sub, "y.go"), "package pkg")

	st, err := store.Open(filepath.Join(root, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sid := "inj-sess"

	// All three turns' responses upfront (FakeProvider consumes them in order).
	r1a, r1b := provider.FakeToolCallResponse("read", map[string]string{"path": "pkg/x.go"}, "done")
	r2a, r2b := provider.FakeToolCallResponse("read", map[string]string{"path": "pkg/y.go"}, "done")
	r3a, r3b := provider.FakeToolCallResponse("read", map[string]string{"path": "pkg/x.go"}, "done")
	a := newCtxAgent(t, root, st, sid, [][]provider.StreamEvent{r1a, r1b, r2a, r2b, r3a, r3b})
	stop := drain(a)
	defer close(stop)

	if err := a.PromptWithContext(context.Background(), "read pkg/x.go"); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	tr := storedToolResults(t, st, sid)
	if !strings.Contains(tr, "PKG-RULES-UNIQUE") {
		t.Errorf("sub AGENTS.md not injected into tool result:\n%s", tr)
	}
	if strings.Contains(tr, "ROOT-RULES") {
		t.Error("cwd AGENTS.md must not be injected (it is in the system prompt)")
	}

	// /status view includes cwd (root) + injected (sub).
	loaded := a.LoadedContextFiles()
	if !hasPath(loaded, filepath.Join(root, "AGENTS.md")) || !hasPath(loaded, filepath.Join(sub, "AGENTS.md")) {
		t.Errorf("LoadedContextFiles missing entries: %v", paths(loaded))
	}

	// Second read from the SAME dir must not re-inject (load once per epoch).
	if err := a.PromptWithContext(context.Background(), "read pkg/y.go"); err != nil {
		t.Fatalf("prompt 2: %v", err)
	}
	// Count occurrences of the unique marker: still exactly 1 across all tool results.
	if n := strings.Count(storedToolResults(t, st, sid), "PKG-RULES-UNIQUE"); n != 1 {
		t.Errorf("sub AGENTS.md injected %d times, want 1 (load once)", n)
	}

	// After a tracker reset (as compaction does), the dir loads again.
	a.resetContextTracker()
	if err := a.PromptWithContext(context.Background(), "read pkg/x.go again"); err != nil {
		t.Fatalf("prompt 3: %v", err)
	}
	if n := strings.Count(storedToolResults(t, st, sid), "PKG-RULES-UNIQUE"); n != 2 {
		t.Errorf("after reset, sub AGENTS.md should re-inject (total 2), got %d", n)
	}
}

// TestContextInjectionDifferentBranch verifies that reading a file outside the
// cwd subtree loads only that file's own dir AGENTS.md, not an ancestor chain.
func TestContextInjectionDifferentBranch(t *testing.T) {
	testutil.TempHome(t)
	base := testutil.TempDir(t)
	cwd := filepath.Join(base, "work")
	other := filepath.Join(base, "elsewhere")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(base, "AGENTS.md"), "BASE-ANCESTOR-RULES") // common ancestor — must NOT load
	writeFile(t, filepath.Join(other, "AGENTS.md"), "OTHER-RULES")
	writeFile(t, filepath.Join(other, "f.txt"), "hi")

	st, err := store.Open(filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sid := "branch-sess"

	first, second := provider.FakeToolCallResponse("read", map[string]string{"path": filepath.Join(other, "f.txt")}, "done")
	a := newCtxAgent(t, cwd, st, sid, [][]provider.StreamEvent{first, second})
	stop := drain(a)
	defer close(stop)

	if err := a.PromptWithContext(context.Background(), "read the other file"); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	tr := storedToolResults(t, st, sid)
	if !strings.Contains(tr, "OTHER-RULES") {
		t.Errorf("file dir AGENTS.md not injected:\n%s", tr)
	}
	if strings.Contains(tr, "BASE-ANCESTOR-RULES") {
		t.Error("common-ancestor AGENTS.md must not be walked for a different-branch file")
	}
}

func hasPath(files []project.ContextFile, p string) bool {
	for _, f := range files {
		if f.Path == p {
			return true
		}
	}
	return false
}

func paths(files []project.ContextFile) []string {
	var out []string
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}
