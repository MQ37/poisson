package tools

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/store"
	"github.com/mq37/poisson/internal/testutil"
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
	want := []string{"bash", "batch", "edit", "glob", "grep", "web_ask", "web_search", "read", "write"}
	for _, w := range want {
		if _, ok := reg.Get(w); !ok {
			t.Errorf("parent registry missing %q; have %v", w, names)
		}
	}
}

// TestBindWebUsage proves the sink it wires is live, not just present: after
// BindWebUsage, actually driving a call through web_search's Anthropic
// backend must reach the callback — the thing agent.ReloadConfigDependentTools
// depends on for every web tool's cost to reach api_calls.
func TestBindWebUsage(t *testing.T) {
	reg := NewRegistry()
	be := &fakeAnthropicWeb{
		searchOut:   "results",
		searchSpend: provider.WebHelperUsage{Usage: provider.Usage{InputTokens: 5}, Model: "claude-haiku-4-5"},
	}
	reg.Register(NewWebSearchTool(be))
	reg.Register(NewFetchTool("", nil))
	reg.Register(NewWebAskTool(nil))

	var got []WebCall
	BindWebUsage(reg, func(c WebCall) { got = append(got, c) })

	webSearch, ok := reg.Get("web_search")
	if !ok {
		t.Fatal("web_search not registered")
	}
	if _, err := webSearch.Execute(context.Background(), mustJSON(t, map[string]any{
		"query": "q", "provider": "anthropic",
	})); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(got) != 1 || got[0].Usage.InputTokens != 5 || got[0].Model != "claude-haiku-4-5" {
		t.Fatalf("got = %+v, want the backend's spend routed through BindWebUsage's sink", got)
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
		SubApproval: func(string, string, string, string, string) (bool, string) {
			return false, ""
		},
	})
	for _, w := range []string{"recall", "subagent"} {
		if _, ok := reg.Get(w); !ok {
			t.Errorf("parent+store registry missing %q", w)
		}
	}
}

// TestBuildRegistry_Child asserts a child gets every tool except subagent,
// including web_ask, web_search, and recall (when a store is supplied).
func TestBuildRegistry_Child(t *testing.T) {
	dir := testutil.TempDir(t)
	st, err := store.Open(filepath.Join(dir, "child.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	reg := BuildRegistry(BuildOptions{
		Cwd:         dir,
		Store:       st,
		Child:       true,
		SubApproval: func(string, string, string, string, string) (bool, string) { return true, "" },
	})
	for _, w := range []string{"read", "write", "edit", "bash", "batch", "grep", "glob", "web_ask", "web_search", "recall"} {
		if _, ok := reg.Get(w); !ok {
			t.Errorf("child registry missing %q; have %v", w, toolNames(reg))
		}
	}
	if _, ok := reg.Get("subagent"); ok {
		t.Error("child registry must never expose subagent (would allow recursion)")
	}
}

func TestRegistry_Unregister(t *testing.T) {
	reg := NewRegistry()
	reg.Register(NewReadTool(".", true, nil))
	if _, ok := reg.Get("read"); !ok {
		t.Fatal("read not registered")
	}
	reg.Unregister("read")
	if _, ok := reg.Get("read"); ok {
		t.Fatal("read still registered after Unregister")
	}
	reg.Unregister("missing") // no-op
}
