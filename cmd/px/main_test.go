package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/config"
	"github.com/mq37/poisson/internal/store"
	"github.com/mq37/poisson/internal/testutil"
)

// TestLoadConfigOrDefaultWarnsOnParseFailure is the regression guard for
// runPrint (-p / headless mode) and runChildMode (subagents) silently
// discarding a config.Load() error and falling back to defaults with zero
// diagnostic — a typo'd config.toml failed loudly only in the interactive
// REPL before this, unlike scripting/CI (-p) or subagent runs.
func TestLoadConfigOrDefaultWarnsOnParseFailure(t *testing.T) {
	tmpHome := testutil.TempHome(t)
	dir := filepath.Join(tmpHome, ".poisson")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Malformed TOML: a bare invalid line the parser rejects outright.
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("not valid toml === ["), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	cfg := loadConfigOrDefault()
	w.Close()
	os.Stderr = oldStderr

	out, _ := io.ReadAll(r)
	if cfg == nil {
		t.Fatal("loadConfigOrDefault returned nil, want DefaultConfig() fallback")
	}
	if !strings.Contains(string(out), "warning: could not load config") {
		t.Errorf("stderr = %q, want a load-failure warning", out)
	}
}

// TestParseArgsModelAndSessionDoNotSwallowNextFlag is the regression guard
// for --model/--session greedily consuming the next argument even when it's
// itself a flag (unlike -p/--print, which already guarded against this).
// Before the fix, "px --session --no-skills -p hi" created a session
// literally named "--no-skills" and silently dropped the --no-skills flag
// the user actually meant.
func TestParseArgsModelAndSessionDoNotSwallowNextFlag(t *testing.T) {
	opts, noSkills, cmdArgs := parseArgs([]string{"--session", "--no-skills", "-p", "hi"})
	if opts.sessionID != "" {
		t.Errorf("sessionID = %q, want empty (--no-skills is a flag, not a value)", opts.sessionID)
	}
	if !noSkills {
		t.Error("--no-skills was swallowed as --session's value instead of being parsed")
	}
	if !opts.print || opts.prompt != "hi" {
		t.Errorf("print=%v prompt=%q, want print=true prompt=\"hi\"", opts.print, opts.prompt)
	}
	_ = cmdArgs

	opts2, _, _ := parseArgs([]string{"--model", "--yolo"})
	if opts2.model != "" {
		t.Errorf("model = %q, want empty (--yolo is a flag, not a value)", opts2.model)
	}
	if !opts2.yolo {
		t.Error("--yolo was swallowed as --model's value instead of being parsed")
	}

	// Real values (not flag-shaped) still pass through as before.
	opts3, _, _ := parseArgs([]string{"--model", "anthropic/claude", "--session", "abc123"})
	if opts3.model != "anthropic/claude" || opts3.sessionID != "abc123" {
		t.Errorf("model=%q sessionID=%q, want anthropic/claude and abc123", opts3.model, opts3.sessionID)
	}
}

func TestResolvePrintRuntimeRestoresExistingSession(t *testing.T) {
	cfg := config.DefaultConfig()
	sess := &store.Session{ID: "s1", Provider: "xai", Model: "grok-build"}

	providerID, model, err := resolvePrintRuntime("", sess, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if providerID != "xai" || model != "grok-build" {
		t.Fatalf("runtime = %s/%s, want xai/grok-build", providerID, model)
	}
}

func TestResolvePrintRuntimeOverrideReplacesPair(t *testing.T) {
	cfg := config.DefaultConfig()
	sess := &store.Session{ID: "s1", Provider: "xai", Model: "grok-build"}

	providerID, model, err := resolvePrintRuntime("openai/gpt-5.5", sess, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if providerID != "openai" || model != "gpt-5.5" {
		t.Fatalf("runtime = %s/%s, want openai/gpt-5.5", providerID, model)
	}
}

func TestResolvePrintRuntimeBareProviderUsesDefault(t *testing.T) {
	cfg := config.DefaultConfig()

	providerID, model, err := resolvePrintRuntime("anthropic", nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if providerID != "anthropic" || model != cfg.Anthropic.Model {
		t.Fatalf("runtime = %s/%s, want anthropic/%s", providerID, model, cfg.Anthropic.Model)
	}
}

func TestResolvePrintRuntimeRejectsIncompletePair(t *testing.T) {
	if _, _, err := resolvePrintRuntime("ollama/", nil, config.DefaultConfig()); err == nil {
		t.Fatal("expected incomplete provider/model error")
	}
}
