package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/config"
)

func validOrchestratorConfig() *config.Config {
	cfg := config.DefaultConfig()
	cfg.Orchestrator.TelegramToken = "test-token"
	cfg.Orchestrator.ChatID = -100
	cfg.Orchestrator.AllowedUserIDs = []string{"111"}
	cfg.Orchestrator.DefaultModel = "anthropic/claude-sonnet-5"
	cfg.Orchestrator.AllowedModels = []string{"anthropic/claude-sonnet-5"}
	cfg.Orchestrator.MaxInstances = 5
	cfg.Orchestrator.StateDir = "/tmp/px-orchestrate-test"
	cfg.Orchestrator.Image = "px-golden"
	return cfg
}

func TestValidateOrchestratorConfig_Valid(t *testing.T) {
	if err := validateOrchestratorConfig(validOrchestratorConfig()); err != nil {
		t.Fatalf("expected a valid config to pass, got %v", err)
	}
}

func TestValidateOrchestratorConfig_MissingToken(t *testing.T) {
	cfg := validOrchestratorConfig()
	cfg.Orchestrator.TelegramToken = ""
	t.Setenv("POISSON_TELEGRAM_TOKEN", "")
	err := validateOrchestratorConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("err = %v, want a token-related error", err)
	}
}

func TestValidateOrchestratorConfig_MissingChatID(t *testing.T) {
	cfg := validOrchestratorConfig()
	cfg.Orchestrator.ChatID = 0
	err := validateOrchestratorConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "chat_id") {
		t.Fatalf("err = %v, want a chat_id-related error", err)
	}
}

func TestValidateOrchestratorConfig_EmptyAllowedUserIDs(t *testing.T) {
	cfg := validOrchestratorConfig()
	cfg.Orchestrator.AllowedUserIDs = nil
	err := validateOrchestratorConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "allowed_user_ids") {
		t.Fatalf("err = %v, want an allowed_user_ids-related error", err)
	}
}

func TestValidateOrchestratorConfig_DefaultModelMissingSlash(t *testing.T) {
	cfg := validOrchestratorConfig()
	cfg.Orchestrator.DefaultModel = "claude-sonnet-5"
	err := validateOrchestratorConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "provider/model") {
		t.Fatalf("err = %v, want a provider/model-format error", err)
	}
}

func TestRenderOrchestratorConfig_NeverPrintsTheRealToken(t *testing.T) {
	cfg := validOrchestratorConfig()
	out := renderOrchestratorConfig(cfg)
	if strings.Contains(out, "test-token") {
		t.Errorf("rendered config leaked the real token: %q", out)
	}
	if !strings.Contains(out, "set (redacted)") {
		t.Errorf("rendered config = %q, want it to report the token is set", out)
	}
	if !strings.Contains(out, "-100") || !strings.Contains(out, "anthropic/claude-sonnet-5") {
		t.Errorf("rendered config = %q, missing expected fields", out)
	}
}

func TestRenderOrchestratorConfig_MissingTokenReported(t *testing.T) {
	cfg := validOrchestratorConfig()
	cfg.Orchestrator.TelegramToken = ""
	t.Setenv("POISSON_TELEGRAM_TOKEN", "")
	out := renderOrchestratorConfig(cfg)
	if !strings.Contains(out, "MISSING") {
		t.Errorf("rendered config = %q, want it to report the token is missing", out)
	}
}

// TestRenderOrchestratorConfig_PrintsHostModeFields is H7's verify
// criterion: --config-check prints allow_host_instances/max_host_instances.
func TestRenderOrchestratorConfig_PrintsHostModeFields(t *testing.T) {
	cfg := validOrchestratorConfig()
	cfg.Orchestrator.AllowHostInstances = true
	cfg.Orchestrator.MaxHostInstances = 2
	out := renderOrchestratorConfig(cfg)
	if !strings.Contains(out, "allow_host_instances: true") {
		t.Errorf("rendered config = %q, want allow_host_instances: true", out)
	}
	if !strings.Contains(out, "max_host_instances: 2") {
		t.Errorf("rendered config = %q, want max_host_instances: 2", out)
	}
}

func TestAcquireOrchestratorLock_SecondAcquireFailsWhileFirstHeld(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "orchestrate.lock")

	release, err := acquireOrchestratorLock(lockPath)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer release()

	if err := os.WriteFile(lockPath, []byte("1"), 0o600); err != nil {
		// pid 1 always exists (init) — simulate "another live process holds it"
		t.Fatal(err)
	}
	if _, err := acquireOrchestratorLock(lockPath); err == nil {
		t.Fatal("expected acquireOrchestratorLock to refuse while pid 1 (always alive) holds the lock")
	}
}

func TestAcquireOrchestratorLock_StaleLockIsOverwritten(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "orchestrate.lock")

	// A pid that (almost certainly) doesn't exist.
	if err := os.WriteFile(lockPath, []byte("999999"), 0o600); err != nil {
		t.Fatal(err)
	}
	release, err := acquireOrchestratorLock(lockPath)
	if err != nil {
		t.Fatalf("expected a stale lock to be silently overwritten, got %v", err)
	}
	release()
}

func TestProcessAlive(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Error("processAlive(self) = false, want true")
	}
	if processAlive(999999) {
		t.Error("processAlive(999999) = true, want false (should not exist)")
	}
}

func TestRunOrchestrate_ConfigCheckExitsCleanlyOnInvalidConfig(t *testing.T) {
	bin := buildPX(t)
	home := isolatedHome(t)
	cmd := exec.Command(bin, "orchestrate", "--config-check")
	cmd.Env = isolatedEnv(home)
	out, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected exit error (no config at all), got %v (output: %s)", err, out)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2 (output: %s)", exitErr.ExitCode(), out)
	}
	if !strings.Contains(string(out), "invalid configuration") {
		t.Errorf("output = %q, want an invalid-configuration message", out)
	}
}

func TestRunOrchestrate_UnknownFlagExits2(t *testing.T) {
	bin := buildPX(t)
	cmd := exec.Command(bin, "orchestrate", "--bogus-flag")
	cmd.Env = isolatedEnv(isolatedHome(t))
	out, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected exit error, got %v (output: %s)", err, out)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2 (output: %s)", exitErr.ExitCode(), out)
	}
}
