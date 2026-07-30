package agent

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/config"
	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/store"
	"github.com/mq37/poisson/internal/testutil"
	"github.com/mq37/poisson/internal/tools"
)

// TestWireNamedToolUseStillRuns covers a real failure: the Anthropic stealth
// path advertises tools under Claude Code's MCP naming convention
// ("mcp_Write"), and a model that emits that wire spelling in a tool_use block
// on a path where the provider didn't unwrap it used to burn a whole round on
// "tool not registered". The turn loop must canonicalize the name and run the
// tool.
func TestWireNamedToolUseStillRuns(t *testing.T) {
	dir := testutil.TempDir(t)
	st, err := store.Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	sid := "wire-tool-name"
	st.CreateSession(&store.Session{ID: sid, Cwd: dir, Provider: "fake", Model: "m", CreatedAt: time.Now().Unix()})

	prov := provider.NewFakeProvider("fake", []provider.Model{{ID: "m", ContextWindow: 8192}})
	first, second := provider.FakeToolCallResponse("mcp_Write", map[string]string{
		"path":    "output.txt",
		"content": "hello world",
	}, "I wrote the file.")
	prov.SetResponses([][]provider.StreamEvent{first, second})

	reg := tools.NewRegistry()
	reg.Register(tools.NewWriteTool(dir, alwaysApprove))

	cfg := config.DefaultConfig()
	cfg.Provider.Default = "fake"
	output := make(chan OutputEvent, 256)
	a := NewAgent(st, prov, reg, cfg, sid, output, nil)
	a.SetModel("m")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a.PromptWithContext(ctx, "write a file")

	for {
		select {
		case ev, ok := <-output:
			if !ok {
				goto check
			}
			if ev.Type == OutputDone {
				goto check
			}
		case <-time.After(10 * time.Second):
			t.Fatal("timeout waiting for the turn")
		}
	}

check:
	data, err := os.ReadFile(dir + "/output.txt")
	if err != nil {
		t.Fatalf("write tool never ran for wire-named tool_use: %v", err)
	}
	if string(data) != "hello world" {
		t.Fatalf("file content = %q, want %q", data, "hello world")
	}
}
