package tools

import (
	"encoding/json"
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
		SubOutput: func(string, string, string, json.RawMessage) {},
		SubApproval: func(string, string, string, string) bool {
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