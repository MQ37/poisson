package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/agent"
	"github.com/mq37/poisson/internal/config"
	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/sandbox"
	"github.com/mq37/poisson/internal/store"
	"github.com/mq37/poisson/internal/testutil"
	"github.com/mq37/poisson/internal/tools"
)

// newTestStoreAndAgentWithSandbox is newTestStoreAndAgent's sandbox-enabled
// sibling: the registry carries real list_sandboxes/sandbox_destroy tools
// backed by a FakeDriver, so /sandbox ls and /sandbox kill can be exercised
// end-to-end through the same tool-lookup path Agent.Tools() uses in
// production, without a real podman install.
func newTestStoreAndAgentWithSandbox(t *testing.T) (*agent.Agent, string, *sandbox.Manager) {
	t.Helper()
	testutil.TempHome(t)
	dir := testutil.TempDir(t)
	dbPath := filepath.Join(dir, "test.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	sessionID := "test-sandbox-cmd-session"
	cfg := config.DefaultConfig()
	if err := s.CreateSession(&store.Session{
		ID: sessionID, Cwd: ".", Provider: "ollama", Model: cfg.Ollama.Model,
		CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	prov := provider.NewFakeProvider("ollama", []provider.Model{{ID: cfg.Ollama.Model, ContextWindow: 8192}})
	mgr := sandbox.NewManager(sandbox.NewFakeDriver())
	reg := tools.BuildRegistry(tools.BuildOptions{Cwd: dir, SandboxManager: mgr})
	a := agent.NewAgent(s, prov, reg, cfg, sessionID, make(chan agent.OutputEvent, 64), func(context.Context, string, string, string) (bool, string) { return false, "" })
	return a, sessionID, mgr
}

func TestCmdSandbox_NoManagerConfigured(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t) // plain registry, no sandbox tools
	tui := newTUIWithAgent(a, sessionID)

	if err := cmdSandbox(cmdHost(tui), []string{"ls"}); err != nil {
		t.Fatal(err)
	}
	out := testScrollOutput(tui)
	if !strings.Contains(out, "not available") {
		t.Errorf("expected a clear 'not available' message, got %q", out)
	}
}

func TestCmdSandbox_UsageWithNoArgs(t *testing.T) {
	a, sessionID, _ := newTestStoreAndAgentWithSandbox(t)
	tui := newTUIWithAgent(a, sessionID)

	if err := cmdSandbox(cmdHost(tui), nil); err != nil {
		t.Fatal(err)
	}
	out := testScrollOutput(tui)
	if !strings.Contains(out, "usage: /sandbox") {
		t.Errorf("expected a usage message, got %q", out)
	}
}

func TestCmdSandboxLs_Empty(t *testing.T) {
	a, sessionID, _ := newTestStoreAndAgentWithSandbox(t)
	tui := newTUIWithAgent(a, sessionID)

	if err := cmdSandbox(cmdHost(tui), []string{"ls"}); err != nil {
		t.Fatal(err)
	}
	out := testScrollOutput(tui)
	if !strings.Contains(out, "no live sandboxes") {
		t.Errorf("expected 'no live sandboxes', got %q", out)
	}
}

func TestCmdSandboxLs_ListsCreatedSandbox(t *testing.T) {
	a, sessionID, mgr := newTestStoreAndAgentWithSandbox(t)
	tui := newTUIWithAgent(a, sessionID)

	if _, err := mgr.Create(context.Background(), sandbox.CreateOpts{Name: "api-testing-2", HostPath: "/tmp/x/workspace"}); err != nil {
		t.Fatal(err)
	}

	if err := cmdSandbox(cmdHost(tui), []string{"ls"}); err != nil {
		t.Fatal(err)
	}
	out := testScrollOutput(tui)
	if !strings.Contains(out, "px-sandbox-api-testing-2") || !strings.Contains(out, "running") {
		t.Errorf("expected the created sandbox listed as running, got %q", out)
	}
}

func TestCmdSandboxKill_DestroysAndReportsError(t *testing.T) {
	a, sessionID, mgr := newTestStoreAndAgentWithSandbox(t)
	tui := newTUIWithAgent(a, sessionID)

	sb, err := mgr.Create(context.Background(), sandbox.CreateOpts{Name: "to-kill"})
	if err != nil {
		t.Fatal(err)
	}

	if err := cmdSandbox(cmdHost(tui), []string{"kill", sb.ID}); err != nil {
		t.Fatal(err)
	}
	out := testScrollOutput(tui)
	if !strings.Contains(out, "destroyed") {
		t.Errorf("expected a 'destroyed' confirmation, got %q", out)
	}
	if mgr.Owns(sb.ID) {
		t.Error("Manager should no longer own the killed sandbox")
	}

	// Killing it again should surface a clean error, not a crash.
	tui2 := newTUIWithAgent(a, sessionID)
	if err := cmdSandbox(cmdHost(tui2), []string{"kill", sb.ID}); err != nil {
		t.Fatal(err)
	}
	out2 := testScrollOutput(tui2)
	if !strings.Contains(out2, "not found") {
		t.Errorf("expected a 'not found' error on double-kill, got %q", out2)
	}
}

func TestCmdSandboxKill_MissingIDShowsUsage(t *testing.T) {
	a, sessionID, _ := newTestStoreAndAgentWithSandbox(t)
	tui := newTUIWithAgent(a, sessionID)

	if err := cmdSandbox(cmdHost(tui), []string{"kill"}); err != nil {
		t.Fatal(err)
	}
	out := testScrollOutput(tui)
	if !strings.Contains(out, "usage: /sandbox kill") {
		t.Errorf("expected a kill usage message, got %q", out)
	}
}

func TestCmdSandbox_UnknownSubcommand(t *testing.T) {
	a, sessionID, _ := newTestStoreAndAgentWithSandbox(t)
	tui := newTUIWithAgent(a, sessionID)

	if err := cmdSandbox(cmdHost(tui), []string{"bogus"}); err != nil {
		t.Fatal(err)
	}
	out := testScrollOutput(tui)
	if !strings.Contains(out, "unknown /sandbox subcommand") {
		t.Errorf("expected an unknown-subcommand message, got %q", out)
	}
}
