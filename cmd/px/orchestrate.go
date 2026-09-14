package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/mq37/poisson/internal/config"
	"github.com/mq37/poisson/internal/orchestrator"
	"github.com/mq37/poisson/internal/orchestrator/hostrt"
	"github.com/mq37/poisson/internal/orchestrator/nspawn"
	"github.com/mq37/poisson/internal/orchestrator/telegram"
)

// runOrchestrate is `px orchestrate`'s entry point: validate config, build
// the Runtime+Frontend+Core, and run until a signal (or the frontend
// itself) stops it. See docs/orchestrator-plan.md Step 26.
func runOrchestrate(args []string) {
	var configCheck, dryRun bool
	for _, a := range args {
		switch a {
		case "--config-check":
			configCheck = true
		case "--dry-run":
			dryRun = true
		default:
			fmt.Fprintf(os.Stderr, "px orchestrate: unknown flag %q\n", a)
			os.Exit(2)
		}
	}

	cfg := loadConfigOrDefault()
	if err := validateOrchestratorConfig(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "px orchestrate: invalid configuration: %v\n", err)
		os.Exit(2)
	}

	if configCheck {
		fmt.Print(renderOrchestratorConfig(cfg))
		return
	}

	// nspawn genuinely needs real root; a --dry-run smoke test (FakeRuntime,
	// no containers at all) needs neither this nor a systemd-nspawn install.
	if !dryRun {
		if os.Geteuid() != 0 {
			fmt.Fprintln(os.Stderr, "px orchestrate: must run as root (systemd-nspawn requires it)")
			os.Exit(1)
		}
		if _, err := exec.LookPath("systemd-nspawn"); err != nil {
			fmt.Fprintln(os.Stderr, "px orchestrate: systemd-nspawn not found on PATH")
			os.Exit(1)
		}
	}

	stateDir := cfg.Orchestrator.StateDir
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "px orchestrate: create state dir %s: %v\n", stateDir, err)
		os.Exit(1)
	}

	// A PID lockfile prevents two orchestrator processes ever polling
	// getUpdates with the same token at once — this is what stops the
	// Telegram 409 Conflict failure mode by construction (see Step 24),
	// rather than merely detecting it after the fact.
	release, err := acquireOrchestratorLock(filepath.Join(stateDir, "orchestrate.lock"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "px orchestrate: %v\n", err)
		os.Exit(1)
	}
	defer release()

	// Both kinds are always built, dry-run or not — CompositeRuntime routes
	// by instance kind, and a dry-run smoke test of /new-host should work
	// with zero real host processes touched, exactly like /new-box already
	// does (see docs/orchestrator-host-mode-plan.md §H7).
	var boxRT, hostRuntime orchestrator.Runtime
	if dryRun {
		boxRT = orchestrator.NewFakeRuntime().WithStateRoot(stateDir)
		hostRuntime = orchestrator.NewFakeRuntime().WithStateRoot(stateDir)
	} else {
		nspawnCfg := nspawn.DefaultConfig()
		nspawnCfg.StateRoot = stateDir
		nspawnCfg.GoldenImage = cfg.Orchestrator.Image
		boxRT = nspawn.NewRuntime(nspawnCfg)

		hostCfg := hostrt.DefaultConfig()
		hostCfg.StateRoot = stateDir
		hostRuntime = hostrt.NewRuntime(hostCfg)
	}
	rt := orchestrator.NewCompositeRuntime(map[string]orchestrator.Runtime{
		orchestrator.KindBox:  boxRT,
		orchestrator.KindHost: hostRuntime,
	})

	client := telegram.NewClient(cfg.ResolvedTelegramToken(), nil)
	fe := telegram.NewFrontend(client, cfg.Orchestrator.ChatID, cfg.Orchestrator.AllowedUserIDs, stateDir)

	provName, modelName, _ := strings.Cut(cfg.Orchestrator.DefaultModel, "/")
	core := orchestrator.NewCore(rt, fe, orchestrator.CoreConfig{
		MaxInstances:       cfg.Orchestrator.MaxInstances,
		DefaultProvider:    provName,
		DefaultModel:       modelName,
		AllowedModels:      cfg.Orchestrator.AllowedModels,
		StateRoot:          stateDir,
		AllowHostInstances: cfg.Orchestrator.AllowHostInstances,
		MaxHostInstances:   cfg.Orchestrator.MaxHostInstances,
	})

	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()
	defer signal.Stop(sigCh)

	if err := core.Run(ctx); err != nil && ctx.Err() == nil {
		// ctx.Err() == nil means Run returned for a reason OTHER than our
		// own signal-triggered cancellation (e.g. the frontend's Run
		// itself failed, such as a fatal Telegram 409) — that's the one
		// case worth a non-zero exit.
		fmt.Fprintf(os.Stderr, "px orchestrate: %v\n", err)
		os.Exit(1)
	}
}

// validateOrchestratorConfig checks every field runOrchestrate actually
// needs before doing anything — a missing token/chat id/default model is a
// clear, specific config-time error, not a confusing runtime failure three
// steps later.
func validateOrchestratorConfig(cfg *config.Config) error {
	if cfg.ResolvedTelegramToken() == "" {
		return fmt.Errorf("no Telegram bot token (set POISSON_TELEGRAM_TOKEN or [orchestrator] telegram_token)")
	}
	if cfg.Orchestrator.ChatID == 0 {
		return fmt.Errorf("[orchestrator] chat_id is not set")
	}
	if len(cfg.Orchestrator.AllowedUserIDs) == 0 {
		return fmt.Errorf("[orchestrator] allowed_user_ids is empty — refusing to start with no one able to command it")
	}
	if cfg.Orchestrator.DefaultModel == "" {
		return fmt.Errorf("[orchestrator] default_model is not set")
	}
	if _, _, ok := strings.Cut(cfg.Orchestrator.DefaultModel, "/"); !ok {
		return fmt.Errorf("[orchestrator] default_model %q must be \"provider/model\"", cfg.Orchestrator.DefaultModel)
	}
	if cfg.Orchestrator.MaxInstances <= 0 {
		return fmt.Errorf("[orchestrator] max_instances must be positive")
	}
	if cfg.Orchestrator.StateDir == "" {
		return fmt.Errorf("[orchestrator] state_dir is not set")
	}
	if cfg.Orchestrator.Image == "" {
		return fmt.Errorf("[orchestrator] image is not set")
	}
	return nil
}

// renderOrchestratorConfig prints the fully resolved configuration for
// --config-check — the token itself is never printed, only whether one is
// present.
func renderOrchestratorConfig(cfg *config.Config) string {
	tokenStatus := "MISSING"
	if cfg.ResolvedTelegramToken() != "" {
		tokenStatus = "set (redacted)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "orchestrator config (resolved):\n")
	fmt.Fprintf(&b, "  telegram_token: %s\n", tokenStatus)
	fmt.Fprintf(&b, "  chat_id: %d\n", cfg.Orchestrator.ChatID)
	fmt.Fprintf(&b, "  allowed_user_ids: %s\n", strings.Join(cfg.Orchestrator.AllowedUserIDs, ", "))
	fmt.Fprintf(&b, "  allowed_models: %s\n", strings.Join(cfg.Orchestrator.AllowedModels, ", "))
	fmt.Fprintf(&b, "  default_model: %s\n", cfg.Orchestrator.DefaultModel)
	fmt.Fprintf(&b, "  max_instances: %d\n", cfg.Orchestrator.MaxInstances)
	fmt.Fprintf(&b, "  state_dir: %s\n", cfg.Orchestrator.StateDir)
	fmt.Fprintf(&b, "  image: %s\n", cfg.Orchestrator.Image)
	fmt.Fprintf(&b, "  allow_host_instances: %v\n", cfg.Orchestrator.AllowHostInstances)
	fmt.Fprintf(&b, "  max_host_instances: %d\n", cfg.Orchestrator.MaxHostInstances)
	return b.String()
}

// acquireOrchestratorLock writes this process's pid to path, refusing if a
// live process already holds it (mirroring the serve.lock concept from
// docs/server-mode-plan.md). A lock file left over from a process that's
// no longer running is stale and silently overwritten — that's the normal
// case after any crash or Restart=always respawn.
func acquireOrchestratorLock(path string) (release func(), err error) {
	if data, err := os.ReadFile(path); err == nil {
		if pid, perr := strconv.Atoi(strings.TrimSpace(string(data))); perr == nil && pid != os.Getpid() && processAlive(pid) {
			return nil, fmt.Errorf("already running (pid %d, lock %s) — refusing to start a second poller against the same Telegram token", pid, path)
		}
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		return nil, fmt.Errorf("write lock file: %w", err)
	}
	return func() { os.Remove(path) }, nil
}

// processAlive reports whether pid refers to a currently running process,
// via a signal-0 probe (sends nothing, just checks deliverability). A
// permission error (the process exists but is owned by another user — the
// common case for an orchestrator lock left by a differently-privileged
// run) still means alive; only ESRCH means it's genuinely gone.
func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	// os.ErrProcessDone is Go's own sentinel for "no such process" (seen in
	// practice, not just syscall.ESRCH directly — confirmed empirically:
	// Go's Process.Signal returns this exact sentinel for a pid that was
	// never real, not a wrapped syscall.Errno).
	if errors.Is(err, os.ErrProcessDone) {
		return false
	}
	var errno syscall.Errno
	if errors.As(err, &errno) && errno == syscall.ESRCH {
		return false
	}
	return true // EPERM or anything else -> exists, just not signalable by us
}
