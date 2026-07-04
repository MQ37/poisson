package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"poisson/internal/config"
	"poisson/internal/project"
	"poisson/internal/provider"
	"poisson/internal/store"
	"poisson/internal/testutil"
	"poisson/internal/tools"
)

// TestLoadedContextFilesGatesAncestors verifies that from a subdirectory a
// parent AGENTS.md is only loaded once a file has been read from that parent.
func TestLoadedContextFilesGatesAncestors(t *testing.T) {
	testutil.TempHome(t)
	root := testutil.TempDir(t)
	sub := filepath.Join(root, "poisson")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "AGENTS.md"), "# root rules")
	writeFile(t, filepath.Join(sub, "AGENTS.md"), "# sub rules")

	st, err := store.Open(filepath.Join(root, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	sid := "ctx-sess"
	cfg := config.DefaultConfig()
	if err := st.CreateSession(&store.Session{
		ID: sid, Cwd: sub, Provider: "ollama", Model: cfg.Ollama.Model,
		CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	prov := provider.NewFakeProvider("ollama", []provider.Model{{ID: cfg.Ollama.Model, ContextWindow: 8192}})
	a := NewAgent(st, prov, tools.NewRegistry(), cfg, sid, make(chan OutputEvent, 8), func(_, _, _ string) bool { return false })

	// Before any read from the parent: only sub's own AGENTS.md (no ~/.poisson).
	files := a.LoadedContextFiles()
	if hasPath(files, filepath.Join(root, "AGENTS.md")) {
		t.Fatalf("parent AGENTS.md must not load from a subdir with no parent read")
	}
	if !hasPath(files, filepath.Join(sub, "AGENTS.md")) {
		t.Fatalf("cwd AGENTS.md should always load; got %v", paths(files))
	}

	// Record a read of a file in the parent dir.
	toolUse, _ := json.Marshal([]map[string]any{{
		"type": "tool_use", "tool_name": "read", "tool_call_id": "c1",
		"tool_input": json.RawMessage(`{"path":"` + filepath.Join(root, "notes.txt") + `"}`),
	}})
	if err := st.AppendMessage(&store.Message{SessionID: sid, Role: "assistant", Content: string(toolUse)}); err != nil {
		t.Fatal(err)
	}

	files = a.LoadedContextFiles()
	if !hasPath(files, filepath.Join(root, "AGENTS.md")) {
		t.Fatalf("parent AGENTS.md should load after a file was read from it; got %v", paths(files))
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
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
