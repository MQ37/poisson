package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"poisson/internal/agent"
	"poisson/internal/config"
	"poisson/internal/provider"
	"poisson/internal/skills"
	"poisson/internal/store"
	"poisson/internal/testutil"
	"poisson/internal/tools"
)

func TestCmdStatus(t *testing.T) {
	testutil.TempHome(t)
	dir := testutil.TempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# Project rules\nBe terse."), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	sid := "status-sess"
	cfg := config.DefaultConfig()
	if err := s.CreateSession(&store.Session{
		ID: sid, Cwd: dir, Provider: "ollama", Model: cfg.Ollama.Model,
		CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	prov := provider.NewFakeProvider("ollama", []provider.Model{{ID: cfg.Ollama.Model, ContextWindow: 8192}})
	reg := tools.NewRegistry()
	reg.Register(tools.NewReadTool(dir))
	reg.Register(tools.NewBashTool(dir, true, func(_, _, _ string) bool { return true }))
	a := agent.NewAgent(s, prov, reg, cfg, sid, make(chan agent.OutputEvent, 64), func(_, _, _ string) bool { return false })
	a.SetModel(cfg.Ollama.Model)
	a.SetSkills(true, []skills.Skill{{Name: "code-quality", Description: "Universal code-quality principles."}})

	tui := newTUIWithAgent(a, sid)
	cmdStatus(cmdHost(tui))
	out := testScrollOutput(tui)

	for _, want := range []string{
		"Session status-sess",
		"Model:", "ollama/",
		"Cwd:", dir,
		"Context:",
		"Tools (", "read", "bash",
		"Context files (1)", filepath.Join(dir, "AGENTS.md"),
		"Skills (1)", "code-quality",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q:\n%s", want, out)
		}
	}
}

func TestCmdStatusSkillsDisabled(t *testing.T) {
	s, a, sid := newTestStoreAndAgent(t)
	_ = s
	a.SetSkills(false, nil)
	tui := newTUIWithAgent(a, sid)
	cmdStatus(cmdHost(tui))
	out := testScrollOutput(tui)
	if !strings.Contains(out, "Skills: (disabled)") {
		t.Errorf("expected disabled skills note:\n%s", out)
	}
}
