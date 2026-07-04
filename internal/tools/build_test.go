package tools

import (
	"path/filepath"
	"testing"

	"poisson/internal/store"
	"poisson/internal/testutil"
)

func toolNames(reg *Registry) []string {
	defs := reg.Definitions()
	names := make([]string, len(defs))
	for i, d := range defs {
		names[i] = d.Name
	}
	return names
}

func TestBuildRegistry_Parent(t *testing.T) {
	dir := testutil.TempDir(t)
	reg := BuildRegistry(BuildOptions{Cwd: dir})
	names := toolNames(reg)
	want := []string{"bash", "edit", "exa_search", "glob", "ls", "read", "search", "write"}
	for _, w := range want {
		if _, ok := reg.Get(w); !ok {
			t.Errorf("parent registry missing %q; have %v", w, names)
		}
	}
}

func TestBuildRegistry_ParentWithStore(t *testing.T) {
	dir := testutil.TempDir(t)
	dbPath := filepath.Join(dir, "test.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	reg := BuildRegistry(BuildOptions{
		Cwd:   dir,
		Store: st,
		SubApproval: func(string, string, string, string, string) bool {
			return false
		},
	})
	for _, w := range []string{"recall", "subagent"} {
		if _, ok := reg.Get(w); !ok {
			t.Errorf("parent+store registry missing %q", w)
		}
	}
}

func TestBuildRegistry_Child(t *testing.T) {
	dir := testutil.TempDir(t)
	reg := BuildRegistry(BuildOptions{Cwd: dir, Tools: "read,bash,search"})
	for _, w := range []string{"read", "bash", "search"} {
		if _, ok := reg.Get(w); !ok {
			t.Errorf("child registry missing %q", w)
		}
	}
	for _, absent := range []string{"write", "subagent", "exa_search"} {
		if _, ok := reg.Get(absent); ok {
			t.Errorf("child registry should not have %q", absent)
		}
	}
}

// TestBuildRegistry_ChildCannotGetSubagent proves the child catalog is a hard
// ceiling: even if "subagent" (or recall/exa_search) is explicitly requested
// in the allowlist, it is never registered, so subagents can't spawn
// subagents (recursion is impossible beyond one level).
func TestBuildRegistry_ChildCannotGetSubagent(t *testing.T) {
	dir := testutil.TempDir(t)
	reg := BuildRegistry(BuildOptions{
		Cwd:         dir,
		Tools:       "read,subagent,recall,exa_search,bash",
		SubApproval: func(_, _, _, _, _ string) bool { return true },
	})
	for _, absent := range []string{"subagent", "recall", "exa_search"} {
		if _, ok := reg.Get(absent); ok {
			t.Errorf("child registry must never expose %q", absent)
		}
	}
	for _, want := range []string{"read", "bash"} {
		if _, ok := reg.Get(want); !ok {
			t.Errorf("child registry missing allowed tool %q", want)
		}
	}
}

func TestRegistry_Unregister(t *testing.T) {
	reg := NewRegistry()
	reg.Register(NewReadTool("."))
	if _, ok := reg.Get("read"); !ok {
		t.Fatal("read not registered")
	}
	reg.Unregister("read")
	if _, ok := reg.Get("read"); ok {
		t.Fatal("read still registered after Unregister")
	}
	reg.Unregister("missing") // no-op
}