package tui

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/agent"
	"github.com/mq37/poisson/internal/config"
	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/store"
	"github.com/mq37/poisson/internal/subagent"
	"github.com/mq37/poisson/internal/testutil"
	"github.com/mq37/poisson/internal/tools"
)

// fakeSleepingChildScript writes a fake child process that just sleeps,
// standing in for a subagent genuinely still running when the session is
// resumed. Mirrors tools.fakeSleepingChildScript (unexported, different
// package).
func fakeSleepingChildScript(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	path := t.TempDir() + "/fake-child-sleep.sh"
	script := "#!/bin/sh\nsleep 30\nprintf '{\"type\":\"done\",\"success\":true}\\n'\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake child script: %v", err)
	}
	return path
}

// hydrateSubagentEnv builds a store + agent (with a real SubagentTool
// registered) and appends a subagent tool_use/tool_result pair recorded as
// an async spawn ack — the only shape a stored subagent tool_result ever
// has (see docs/async-subagent-plan.md).
func hydrateSubagentEnv(t *testing.T) (*store.Store, *tools.SubagentTool, *agent.Agent, string) {
	t.Helper()
	dir := testutil.TempDir(t)
	st, err := store.Open(dir + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	sid := "hydrate-subagent-test"
	if err := st.CreateSession(&store.Session{ID: sid, Cwd: ".", Provider: "fake", Model: "m"}); err != nil {
		t.Fatal(err)
	}

	subagentTool := tools.NewSubagentTool(".", func(string, string, string, string, string) (bool, string) { return true, "" })
	subagentTool.SetRuntime(func() string { return "anthropic" }, func() string { return "claude-opus-5" }, func() string { return "" })
	reg := tools.NewRegistry()
	reg.Register(subagentTool)

	a := agent.NewAgent(st, provider.NewFakeProvider("fake", nil), reg, config.DefaultConfig(), sid, nil, nil)
	return st, subagentTool, a, sid
}

func appendSubagentAckMessages(t *testing.T, st *store.Store, sid, toolCallID, ack string) {
	t.Helper()
	toolUse, _ := json.Marshal([]map[string]any{{
		"type": "tool_use", "tool_call_id": toolCallID, "tool_name": "subagent",
		"tool_input": json.RawMessage(`{"task":"look around","name":"scout"}`),
	}})
	if err := st.AppendMessage(&store.Message{SessionID: sid, Role: "assistant", Content: string(toolUse)}); err != nil {
		t.Fatal(err)
	}
	toolResult, _ := json.Marshal([]map[string]any{{
		"type": "tool_result", "tool_call_id": toolCallID, "tool_result": ack,
	}})
	if err := st.AppendMessage(&store.Message{SessionID: sid, Role: "tool", Content: string(toolResult)}); err != nil {
		t.Fatal(err)
	}
}

// TestHydrateKeepsRunningJobWidgetLive is the regression guard for bug 11: a
// job that's genuinely still running (per Agent.SubagentJobLive) must not be
// marked done on resume just because its stored tool_result is the spawn
// ack — the only thing ever persisted for an async subagent call.
func TestHydrateKeepsRunningJobWidgetLive(t *testing.T) {
	scriptPath := fakeSleepingChildScript(t)
	restore := subagent.SetLookupExecutableForTest(scriptPath)
	defer restore()

	st, subagentTool, a, sid := hydrateSubagentEnv(t)

	res, err := subagentTool.Execute(context.Background(), json.RawMessage(`{"task":"look around","name":"scout"}`))
	if err != nil || res.Error != "" {
		t.Fatalf("Execute: res=%+v err=%v", res, err)
	}
	// Give runJob a moment to actually spawn the child (status flips to
	// "running" only once Spawn succeeds).
	time.Sleep(200 * time.Millisecond)

	appendSubagentAckMessages(t, st, sid, "call-1", res.Content)

	tui := newTUI(a, sid, nil)
	tui.mu.Lock()
	tui.resetSessionViewLocked()
	streaming := tui.scroll.blocks[0].meta.Streaming
	toolDone := tui.scroll.blocks[0].meta.ToolDone
	resumedLive := tui.scroll.blocks[0].meta.ResumedLiveJob
	tui.mu.Unlock()

	if toolDone {
		t.Fatal("widget marked done on resume even though the job is genuinely still running")
	}
	if !streaming {
		t.Fatal("widget not left running")
	}
	if !resumedLive {
		t.Fatal("ResumedLiveJob not set — finalizeOrphanSubagents would force this done anyway")
	}

	subagentTool.KillAll()
}

// TestHydrateCompletesWidgetWhenJobNoLongerKnown covers the ordinary case:
// the job named in the stored ack is unknown (process restarted since, or a
// stale/garbage id) — the widget must still complete on resume exactly as
// before this fix, not spin forever.
func TestHydrateCompletesWidgetWhenJobNoLongerKnown(t *testing.T) {
	_, _, a, sid := hydrateSubagentEnv(t)
	st := a.Store()

	ack := `Subagent "scout" spawned as job sub-does-not-exist. It runs in the background — use subagent_status to check progress, subagent_result to retrieve the final output once done.`
	appendSubagentAckMessages(t, st, sid, "call-1", ack)

	tui := newTUI(a, sid, nil)
	tui.mu.Lock()
	tui.resetSessionViewLocked()
	toolDone := tui.scroll.blocks[0].meta.ToolDone
	resumedLive := tui.scroll.blocks[0].meta.ResumedLiveJob
	tui.mu.Unlock()

	if !toolDone {
		t.Fatal("widget should complete when the job is no longer known")
	}
	if resumedLive {
		t.Fatal("ResumedLiveJob must not be set for an unknown job")
	}
}
