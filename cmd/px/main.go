package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/mq37/poisson/internal/agent"
	"github.com/mq37/poisson/internal/auth"
	"github.com/mq37/poisson/internal/config"
	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/sandbox"
	"github.com/mq37/poisson/internal/skills"
	"github.com/mq37/poisson/internal/store"
	"github.com/mq37/poisson/internal/subagent"
	"github.com/mq37/poisson/internal/tools"
	"github.com/mq37/poisson/internal/tui"
)

// newSandboxManager returns a Manager backed by the real podman CLI, no
// storage confinement (production uses whatever podman storage the host
// user already has configured — confinement is a test-only concern, see
// docs/sandbox-plan.md's disk-wear guard). Never fails eagerly: podman not
// being installed only surfaces once a sandbox tool actually tries to use
// it, the same way a missing `rg` only surfaces when grep is called.
//
// sessionID is stamped as a label on every sandbox this Manager creates
// (list_sandboxes shows it); discovery is enabled unconditionally — a
// top-level session (headless -p or the REPL) is exactly the case that
// should be able to find and reattach to any live sandbox by name,
// including one from a crashed process or a different session (see
// docs/sandbox-plan.md's "Crash recovery" section). Only
// resolveChildSandboxManager (a subagent's own Manager) must never call
// EnableDiscovery — it builds its own Manager directly instead of calling
// this, so that omission can't be accidentally inherited from here.
func newSandboxManager(sessionID string) *sandbox.Manager {
	mgr := sandbox.NewManager(sandbox.NewPodmanDriver(nil, nil))
	mgr.SetSessionID(sessionID)
	mgr.EnableDiscovery()
	return mgr
}

// resolveChildSandboxManager parses envValue (POISSON_SUBAGENT_SANDBOXES, as
// built by subagent.buildSpawnEnv) and returns a Manager with each
// authorized sandbox recorded via Authorize, or nil when there's nothing to
// authorize — a subagent given no sandboxes (the common case: most spawns
// carry none) must not even see sandbox_cp/sandbox_destroy as available
// tools, same reasoning as a session with no sandbox support at all (see
// build.go). A malformed value is treated the same as empty: never lets a
// subagent attempt to use sandboxing off of unparseable input. Pulled out of
// runChildMode as its own function for the same testability reason
// subagent.buildSpawnArgs/buildSpawnEnv were pulled out of Spawn.
func resolveChildSandboxManager(envValue string) *sandbox.Manager {
	authorized, err := subagent.ParseAuthorizedSandboxes(envValue)
	if err != nil || len(authorized) == 0 {
		return nil
	}
	// Built directly, not via newSandboxManager: a subagent's Manager must
	// never have discovery enabled — it may only ever use exactly what its
	// parent Authorize'd below, never anything it could find on its own.
	mgr := sandbox.NewManager(sandbox.NewPodmanDriver(nil, nil))
	now := time.Now()
	for _, sa := range authorized {
		mgr.Authorize(sandbox.Sandbox{ID: sa.ID, HostPath: sa.HostPath, CreatedAt: now, LastUsed: now})
	}
	return mgr
}

const version = "v0.1.0"

func main() {
	// Child subagent mode.
	if os.Getenv("POISSON_SUBAGENT_CHILD") == "1" {
		runChildMode()
		return
	}

	noSkills := false
	var opts printOpts
	var cmdArgs []string
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--no-skills":
			noSkills = true
		case "--yolo":
			opts.yolo = true
		case "-p", "--print":
			opts.print = true
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				opts.prompt = args[i+1]
				i++
			}
		case "--model":
			if i+1 < len(args) {
				opts.model = args[i+1]
				i++
			}
		case "--session":
			if i+1 < len(args) {
				opts.sessionID = args[i+1]
				i++
			}
		default:
			cmdArgs = append(cmdArgs, args[i])
		}
	}

	if opts.print {
		opts.noSkills = noSkills
		if opts.prompt == "" {
			opts.prompt = strings.TrimSpace(strings.Join(cmdArgs, " "))
		}
		if opts.prompt == "" {
			opts.prompt = readStdin()
		}
		if strings.TrimSpace(opts.prompt) == "" {
			fmt.Fprintln(os.Stderr, "px -p: no prompt (pass a string or pipe via stdin)")
			os.Exit(2)
		}
		runPrint(opts)
		return
	}

	if len(cmdArgs) == 0 {
		runREPL(noSkills, "")
		return
	}

	switch cmdArgs[0] {
	case "login":
		cmdLogin(cmdArgs[1:])
	case "logout":
		cmdLogout(cmdArgs[1:])
	case "-v", "--version", "version":
		fmt.Println("poisson", version)
	case "sessions":
		cmdSessions()
	case "cost":
		cmdCost(cmdArgs[1:])
	case "resume":
		if len(cmdArgs) < 2 || strings.TrimSpace(cmdArgs[1]) == "" {
			fmt.Fprintln(os.Stderr, "usage: Poisson resume <session-id>")
			os.Exit(2)
		}
		runREPL(noSkills, cmdArgs[1])
	default:
		fmt.Println("poisson", version)
		fmt.Println("usage: Poisson [command] [options]")
		fmt.Println("  Poisson                     interactive REPL")
		fmt.Println("  Poisson login <provider>    OAuth login")
		fmt.Println("  Poisson logout <provider>   clear stored tokens")
		fmt.Println("  Poisson sessions            list sessions")
		fmt.Println("  Poisson resume <session-id> resume a session in the TUI")
		fmt.Println("  Poisson cost [session-id]   show cost")
		fmt.Println("  Poisson -v                  print version")
	}
}

// printOpts configures headless single-prompt (-p) mode.
type printOpts struct {
	print     bool
	yolo      bool
	noSkills  bool
	prompt    string
	model     string // "provider/model" (or bare "provider"); empty = config default
	sessionID string // reuse an existing session id
}

// readStdin slurps all of stdin (for `px -p < file` / pipelines).
func readStdin() string {
	data, _ := io.ReadAll(os.Stdin)
	return string(data)
}

func resolvePrintRuntime(modelArg string, sess *store.Session, cfg *config.Config) (string, string, error) {
	if modelArg == "" && sess != nil {
		if sess.Provider == "" || sess.Model == "" {
			return "", "", fmt.Errorf("session %s has no provider/model", sess.ID)
		}
		return sess.Provider, sess.Model, nil
	}

	provName := cfg.Provider.Default
	model := ""
	if modelArg != "" {
		if p, m, ok := strings.Cut(modelArg, "/"); ok {
			provName, model = strings.TrimSpace(p), strings.TrimSpace(m)
			if provName == "" || model == "" {
				return "", "", fmt.Errorf("invalid model %q; use provider/model", modelArg)
			}
		} else {
			provName = strings.TrimSpace(modelArg)
		}
	}
	if model == "" {
		model = provider.DefaultModel(provName, cfg)
	}
	if provName == "" || model == "" {
		return "", "", fmt.Errorf("no model configured for provider %q", provName)
	}
	return provName, model, nil
}

// runPrint runs a single prompt headlessly: it streams the assistant's text to
// stdout and tool activity to stderr, then exits. Read-only tools auto-run;
// risky bash is denied unless --yolo. Used for scripting and pipelines.
func runPrint(opts printOpts) {
	cfg, err := config.Load()
	if err != nil || cfg == nil {
		cfg = config.DefaultConfig()
	}
	dbPath := filepath.Join(config.ConfigDir(), "poisson.db")
	st, err := store.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error opening database: %v\n", err)
		os.Exit(1)
	}
	defer st.Close()

	authStore, _ := auth.Load()

	cwd, _ := os.Getwd()
	sessionID := opts.sessionID
	if sessionID == "" {
		sessionID = store.NewSessionID()
	}
	var sess *store.Session
	if existing, err := st.GetSession(sessionID); err == nil {
		sess = existing
	} else if !errors.Is(err, store.ErrNotFound) {
		fmt.Fprintf(os.Stderr, "error reading session: %v\n", err)
		os.Exit(1)
	}

	provName, model, err := resolvePrintRuntime(opts.model, sess, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "px -p: %v\n", err)
		os.Exit(2)
	}
	prov := provider.NewProvider(provName, authStore, cfg)
	if prov == nil {
		fmt.Fprintf(os.Stderr, "px -p: unknown provider %q (use anthropic/<model>, openai/<model>, xai/<model>, ollama/<model>)\n", provName)
		os.Exit(2)
	}

	if sess == nil {
		if err := st.CreateSession(&store.Session{
			ID: sessionID, Cwd: cwd, Provider: provName, Model: model, CreatedAt: time.Now().Unix(),
		}); err != nil {
			fmt.Fprintf(os.Stderr, "error creating session: %v\n", err)
			os.Exit(1)
		}
	} else if sess.Provider != provName || sess.Model != model {
		// Persist the pair in one UPDATE. Writing provider and model separately can
		// leave an impossible combination if the second write fails.
		sess.Provider, sess.Model = provName, model
		if err := st.UpdateSession(sess); err != nil {
			fmt.Fprintf(os.Stderr, "error updating session model: %v\n", err)
			os.Exit(1)
		}
	}

	yolo := opts.yolo
	var agentRef *agent.Agent
	humanApproval := func(ctx context.Context, command, description, workdir string, risk agent.BashRisk, origin agent.ApprovalOrigin) (bool, string) {
		return yolo, "" // headless: only --yolo approves escalated commands; no live human to mark
	}
	approvalFn := func(ctx context.Context, command, description, workdir string) (bool, string) {
		if agentRef != nil {
			return agent.WrapRiskGatedApproval(agentRef, humanApproval)(ctx, command, description, workdir)
		}
		return humanApproval(ctx, command, description, workdir, agent.BashRiskUnknown, agent.ApprovalOriginMain)
	}
	// Sensitive files (.env*, SSH/cloud credentials, ~/.poisson secrets, ...)
	// are deterministically flagged by guard.SensitivePathReason, so this asks
	// the human directly — no LLM risk classification needed.
	fileApprovalFn := func(ctx context.Context, action, reason, workdir string) (bool, string) {
		return humanApproval(ctx, action, reason, workdir, agent.BashRiskHigh, agent.ApprovalOriginFromContext(ctx))
	}
	// create_sandbox asking for mounts/env beyond its own scratch workspace
	// is exactly the same "sensitive, ask the human directly" shape as
	// fileApprovalFn — see docs/sandbox-plan.md's "Approval" section.
	sandboxApprovalFn := func(ctx context.Context, action, reason, workdir string) (bool, string) {
		return humanApproval(ctx, action, reason, workdir, agent.BashRiskHigh, agent.ApprovalOriginFromContext(ctx))
	}
	reg := tools.BuildRegistry(tools.BuildOptions{
		Cwd:               cwd,
		Store:             st,
		Auth:              authStore,
		ApprovalFn:        approvalFn,
		FileApprovalFn:    fileApprovalFn,
		SandboxManager:    newSandboxManager(sessionID),
		SandboxApprovalFn: sandboxApprovalFn,
	})

	outputChan := make(chan agent.OutputEvent, 256)
	a := agent.NewAgent(st, prov, reg, cfg, sessionID, outputChan, approvalFn)
	agentRef = a
	if err := a.SetModel(model); err != nil {
		fmt.Fprintf(os.Stderr, "error updating session model: %v\n", err)
		os.Exit(1)
	}
	var skillList []skills.Skill
	if !opts.noSkills {
		skillList, _ = skills.Discover()
	}
	a.SetSkills(!opts.noSkills, skillList)
	a.ReloadConfigDependentTools()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for ev := range outputChan {
			switch ev.Type {
			case agent.OutputText:
				fmt.Print(ev.Text)
			case agent.OutputToolStart:
				fmt.Fprintf(os.Stderr, "[tool: %s]\n", ev.ToolName)
			case agent.OutputError:
				fmt.Fprintf(os.Stderr, "\n[error: %s]\n", ev.Text)
			}
		}
	}()

	runErr := a.Prompt(opts.prompt)
	close(outputChan)
	wg.Wait()
	fmt.Println()
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", runErr)
		os.Exit(1)
	}
}

// runREPL starts the interactive TUI. When resumeSessionID is non-empty
// (from `px resume <id>`), it must already exist in the store — checked
// here, before any provider/agent setup, so a typo fails fast with a clean
// exit(1) instead of opening a REPL around an ephemeral throwaway session.
// The actual switch (provider/model, session id, scrollback hydration) is
// done post-construction via TUI.ResumeAtStartup, reusing the same
// cmdResume path the /resume slash command already exercises.
func runREPL(noSkills bool, resumeSessionID string) {
	// Load config.
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not load config: %v\n", err)
		cfg = config.DefaultConfig()
	}

	// Open store.
	dbPath := filepath.Join(config.ConfigDir(), "poisson.db")
	st, err := store.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error opening database: %v\n", err)
		os.Exit(1)
	}
	defer st.Close()

	if resumeSessionID != "" {
		if _, err := st.GetSession(resumeSessionID); err != nil {
			fmt.Fprintf(os.Stderr, "error: session not found: %s\n", resumeSessionID)
			os.Exit(1)
		}
	}

	// Load auth.
	authStore, _ := auth.Load()

	prov, provName, model, warn := provider.BootstrapFromConfig(authStore, cfg)
	if warn != "" {
		fmt.Fprintln(os.Stderr, warn)
	}
	if prov == nil {
		fmt.Fprintf(os.Stderr, "error: unknown provider %q in config; use anthropic, ollama, or xai\n", provName)
		os.Exit(1)
	}

	// Ephemeral session id until the user sends the first message.
	sessionID := store.NewSessionID()
	cwd, _ := os.Getwd()

	// Set up output channel.
	outputChan := make(chan agent.OutputEvent, 256)

	// Approval callback for dangerous bash commands. It delegates to the TUI
	// (set below) so the prompt owns stdin exclusively in blocking mode; the
	// terminal runs raw with a nonblocking Ctrl+C poller otherwise.
	var approveUI tui.Approver
	var agentRef *agent.Agent
	humanApproval := func(ctx context.Context, command, description, workdir string, risk agent.BashRisk, origin agent.ApprovalOrigin) (bool, string) {
		if approveUI != nil {
			return approveUI.Approve(ctx, command, description, workdir, risk, origin)
		}
		return false, ""
	}
	approvalFn := func(ctx context.Context, command, description, workdir string) (bool, string) {
		if agentRef != nil {
			return agent.WrapRiskGatedApproval(agentRef, humanApproval)(ctx, command, description, workdir)
		}
		return humanApproval(ctx, command, description, workdir, agent.BashRiskUnknown, agent.ApprovalOriginFromContext(ctx))
	}
	// Sensitive files (.env*, SSH/cloud credentials, ~/.poisson secrets, ...)
	// are deterministically flagged by guard.SensitivePathReason, so this asks
	// the human directly — no LLM risk classification needed.
	fileApprovalFn := func(ctx context.Context, action, reason, workdir string) (bool, string) {
		return humanApproval(ctx, action, reason, workdir, agent.BashRiskHigh, agent.ApprovalOriginFromContext(ctx))
	}

	// No ctx here — the relay from a subagent child has no in-flight Go
	// context tied to a toolCallID in this process's registry, so it carries
	// no ApprovalRecord either. RecordApproval degrades to a no-op, matching
	// that a subagent's internal commands never get their own tool card in
	// the main conversation (only the aggregate subagent widget) — nothing
	// to mark.
	subApprovalFn := func(command, description, workdir, agentName, risk string) (bool, string) {
		return humanApproval(context.Background(), command, description, workdir, agent.ParseBashRisk(risk), agent.SubagentOrigin(agentName))
	}
	// create_sandbox asking for mounts/env beyond its own scratch workspace
	// is exactly the same "sensitive, ask the human directly" shape as
	// fileApprovalFn — see docs/sandbox-plan.md's "Approval" section.
	sandboxApprovalFn := func(ctx context.Context, action, reason, workdir string) (bool, string) {
		return humanApproval(ctx, action, reason, workdir, agent.BashRiskHigh, agent.ApprovalOriginFromContext(ctx))
	}

	reg := tools.BuildRegistry(tools.BuildOptions{
		Cwd:               cwd,
		Store:             st,
		Auth:              authStore,
		ApprovalFn:        approvalFn,
		FileApprovalFn:    fileApprovalFn,
		SubApproval:       subApprovalFn,
		SandboxManager:    newSandboxManager(sessionID),
		SandboxApprovalFn: sandboxApprovalFn,
	})

	// Set up agent.
	a := agent.NewAgent(st, prov, reg, cfg, sessionID, outputChan, approvalFn)
	agentRef = a
	tools.BindSubagentRuntime(reg, func() string { return a.Provider().ID() }, func() string { return a.Model() }, func() string { return a.Effort() })
	tools.BindSubagentProgress(reg, a.SendSubagentProgress)
	tools.BindSubagentSkills(reg, a.SkillsEnabled)
	tools.BindSubagentUsage(reg, a.RecordSubagentUsage)
	tools.BindSubagentClassifier(reg, a.ClassifierModel)
	tools.BindBatchSubagentDone(reg, a.CompleteBatchedSubagent)

	var skillList []skills.Skill
	if !noSkills {
		var err error
		skillList, err = skills.Discover()
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: skills discover: %v\n", err)
		}
	}
	a.SetSkills(!noSkills, skillList)
	a.ReloadConfigDependentTools()

	// Run TUI.
	t := tui.NewTUI(a, sessionID, outputChan)
	t.InstallStartupIntro(version, provName, model)
	if resumeSessionID != "" {
		t.ResumeAtStartup(resumeSessionID)
	}
	approveUI = t
	// A message queued while a turn is running is spliced into that same
	// turn's next iteration (see agent.SetPendingInputFn's doc comment)
	// instead of only being sent once the whole turn finishes.
	a.SetPendingInputFn(t.TakeQueuedForInjection)
	runErr := t.Run()
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", runErr)
	}
}

// cmdSessions lists sessions from the CLI.
func cmdSessions() {
	dbPath := filepath.Join(config.ConfigDir(), "poisson.db")
	st, err := store.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer st.Close()

	sessions, err := st.ListSessions(20, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if len(sessions) == 0 {
		fmt.Println("no sessions")
		return
	}
	for _, s := range sessions {
		date := time.Unix(s.CreatedAt, 0).Format("2006-01-02")
		msgs, _ := st.GetMessages(s.ID)
		fmt.Printf("  %s  %s  %d msgs  %s/%s\n", store.DisplaySessionID(s.ID), date, len(msgs), s.Provider, s.Model)
	}
}

// cmdCost shows cost from the CLI.
func cmdCost(args []string) {
	dbPath := filepath.Join(config.ConfigDir(), "poisson.db")
	st, err := store.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer st.Close()

	if len(args) > 0 {
		sid := args[0]
		if _, err := st.GetSession(sid); err != nil {
			fmt.Fprintf(os.Stderr, "error: session not found: %s\n", sid)
			os.Exit(1)
		}
		cost, err := st.GetSessionCost(sid)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error reading cost: %v\n", err)
			os.Exit(1)
		}
		breakdown, err := st.GetSessionTokenBreakdown(sid)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error reading token breakdown: %v\n", err)
			os.Exit(1)
		}
		fmt.Print(breakdown.FormatCost(sid, cost))
		return
	}

	cost, err := st.GetTotalCost()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading total cost: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Total cost across all sessions: $%.4f\n", cost)
}

func cmdLogin(args []string) {
	if len(args) == 0 {
		fmt.Println("usage: Poisson login <provider>")
		fmt.Println("providers: anthropic, xai, openai")
		return
	}
	prov := strings.ToLower(args[0])
	authStore, err := auth.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading auth: %v\n", err)
		os.Exit(1)
	}

	switch prov {
	case "anthropic":
		fmt.Println("Starting Anthropic OAuth login (Claude Pro/Max)...")
		entry, err := auth.LoginAnthropic()
		if err != nil {
			fmt.Fprintf(os.Stderr, "login failed: %v\n", err)
			os.Exit(1)
		}
		authStore["anthropic"] = *entry
		if err := auth.Save(authStore); err != nil {
			fmt.Fprintf(os.Stderr, "error saving auth: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Logged in to Anthropic (Claude Pro/Max).")
		fmt.Println("Stealth mode active — requests will use subscription quota.")

	case "xai":
		fmt.Println("Starting xAI device-code login...")
		entry, err := auth.LoginXAI()
		if err != nil {
			fmt.Fprintf(os.Stderr, "login failed: %v\n", err)
			os.Exit(1)
		}
		authStore["xai"] = *entry
		if err := auth.Save(authStore); err != nil {
			fmt.Fprintf(os.Stderr, "error saving auth: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Logged in to xAI (SuperGrok).")

	case "openai":
		fmt.Println("Starting OpenAI Codex OAuth login (ChatGPT Plus/Pro)...")
		entry, err := auth.LoginOpenAI()
		if err != nil {
			fmt.Fprintf(os.Stderr, "login failed: %v\n", err)
			os.Exit(1)
		}
		authStore["openai"] = *entry
		if err := auth.Save(authStore); err != nil {
			fmt.Fprintf(os.Stderr, "error saving auth: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Logged in to OpenAI (ChatGPT subscription). Model: gpt-5.5")

	case "ollama":
		fmt.Println("Ollama runs locally and needs no login.")
		fmt.Println("Just start the Ollama server (default: http://localhost:11434).")

	default:
		fmt.Printf("unknown provider: %s\n", prov)
		os.Exit(1)
	}
}

func cmdLogout(args []string) {
	if len(args) == 0 {
		fmt.Println("usage: Poisson logout <provider>")
		return
	}
	prov := strings.ToLower(args[0])
	authStore, err := auth.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading auth: %v\n", err)
		os.Exit(1)
	}
	delete(authStore, prov)
	if err := auth.Save(authStore); err != nil {
		fmt.Fprintf(os.Stderr, "error saving auth: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Logged out of %s.\n", prov)
}

// runChildMode runs Poisson as a subagent child process in JSON output mode.
// It reads a task from args, runs the agent, and writes JSON events to
// stdout. Bash approval requests are written to stdout and the response is
// read from stdin.
// subagentNetworkRetryBudget bounds how long a subagent keeps retrying a
// network failure (see provider.RetryTrace.MaxElapsed) before giving up and
// reporting an error — unlike the interactive main session, which retries
// indefinitely since a human is watching and can Ctrl+C.
const subagentNetworkRetryBudget = 3 * time.Minute

func runChildMode() {
	// A panic anywhere below (agent loop, a tool, provider parsing, etc.)
	// would otherwise crash this process bare: the parent's ReadEvent()
	// only sees its stdout pipe close and reports a bare "EOF" with zero
	// diagnostic value (the panic's actual value/stack print to os.Stderr,
	// which is inherited from the PARENT's terminal — never captured, gone
	// the moment it happens). The interactive TUI already recovers panics
	// the same way (internal/tui/agent_io.go, lifecycle.go); child mode
	// never got the same treatment. Recovering here and reporting a real
	// "error" event turns the next occurrence into something diagnosable.
	defer func() {
		if r := recover(); r != nil {
			recoverChildPanic(writeChildEvent, "run", r)
			os.Exit(1)
		}
	}()

	// Parse args: --json [--no-skills] --session <id> [-- task]
	var sessionID, task string
	var noSkills bool
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
		case "--no-skills":
			noSkills = true
		case "--session":
			if i+1 < len(args) {
				sessionID = args[i+1]
				i++
			}
		case "--":
			task = strings.Join(args[i+1:], " ")
			i = len(args)
		default:
			if task == "" {
				task = args[i]
			} else {
				task += " " + args[i]
			}
		}
	}

	if task == "" {
		fmt.Fprintln(os.Stderr, "child mode: no task provided")
		os.Exit(1)
	}

	// Load config + open store.
	cfg, _ := config.Load()
	if cfg == nil {
		cfg = config.DefaultConfig()
	}
	// Subagents run against an ephemeral DB (POISSON_SUBAGENT_DB) so their
	// conversation is never persisted to the user's real DB.
	dbPath := os.Getenv("POISSON_SUBAGENT_DB")
	if dbPath == "" {
		dbPath = filepath.Join(config.ConfigDir(), "poisson.db")
	}
	st, err := store.Open(dbPath)
	if err != nil {
		writeChildEvent(map[string]interface{}{"type": "error", "error": err.Error()})
		os.Exit(1)
	}
	defer st.Close()

	// Create or use existing session.
	if sessionID == "" {
		sessionID = store.NewSubagentID()
	}
	cwd, _ := os.Getwd()
	childProv := os.Getenv("POISSON_SUBAGENT_PROVIDER")
	childModel := os.Getenv("POISSON_SUBAGENT_MODEL")
	if childProv == "" {
		childProv = cfg.Provider.Default
	}
	if _, ok := config.ProviderMetaByID(childProv); !ok {
		childProv = "ollama"
	}
	if childModel == "" {
		childModel = provider.DefaultModel(childProv, cfg)
	}
	if _, err := st.GetSession(sessionID); err != nil {
		if err := st.CreateSession(&store.Session{
			ID:         sessionID,
			Cwd:        cwd,
			Provider:   childProv,
			Model:      childModel,
			IsSubagent: true,
		}); err != nil {
			writeChildEvent(map[string]interface{}{"type": "error", "error": err.Error()})
			os.Exit(1)
		}
	}

	authStore, _ := auth.Load()
	prov, _, _, _ := provider.BootstrapFromConfig(authStore, cfg)
	if prov == nil || prov.ID() != childProv {
		prov = provider.NewProvider(childProv, authStore, cfg)
	}

	var approvalBroker childApprovalBroker

	var childAgentRef *agent.Agent
	// origin is unused here: the parent process is the one that labels this
	// approval as coming from a subagent, via the "agent" field already sent
	// below and read back into agent.SubagentOrigin by subApprovalFn in the
	// parent's own main().
	humanChildApproval := func(ctx context.Context, command, description, workdir string, risk agent.BashRisk, origin agent.ApprovalOrigin) (bool, string) {
		event := map[string]interface{}{
			"type":        "approval_request",
			"command":     command,
			"description": description,
			"cwd":         workdir,
			"risk":        string(risk),
			"agent":       os.Getenv("POISSON_SUBAGENT_NAME"),
		}
		// Usage travels with the prompt so the risk-classification call that
		// produced this very verdict is already banked on the parent side (see
		// ChildEvent.Usage). Otherwise those tokens sit unreported until the
		// next "tool"/"done" event — and a parent turn cancelled while the
		// human is still deciding never sees either, silently dropping the
		// classifier's spend.
		if childAgentRef != nil {
			usage := childAgentRef.CumulativeUsage()
			event["usage"] = usage
			event["cost"] = childAgentRef.CumulativeCost()
		}
		return approvalBroker.emitAndWait(event)
	}
	approvalFn := func(ctx context.Context, command, description, workdir string) (bool, string) {
		if childAgentRef != nil {
			return agent.WrapRiskGatedApproval(childAgentRef, humanChildApproval)(ctx, command, description, workdir)
		}
		return humanChildApproval(ctx, command, description, workdir, agent.BashRiskUnknown, agent.ApprovalOriginMain)
	}
	fileApprovalFn := func(ctx context.Context, action, reason, workdir string) (bool, string) {
		return humanChildApproval(ctx, action, reason, workdir, agent.BashRiskHigh, agent.ApprovalOriginMain)
	}

	// A subagent never mints its own sandbox (create_sandbox is excluded
	// from a Child registry regardless — see build.go), it can only use
	// ones its parent explicitly authorized via POISSON_SUBAGENT_SANDBOXES
	// (see docs/sandbox-plan.md's subagent allow-list).
	childSandboxMgr := resolveChildSandboxManager(os.Getenv("POISSON_SUBAGENT_SANDBOXES"))

	// Child:true grants every tool except subagent, so a subagent gets the full
	// tool set (read/write/edit/bash/web_search/web_ask/recall)
	// but cannot spawn further subagents — recursion is bounded to one level.
	reg := tools.BuildRegistry(tools.BuildOptions{
		Cwd:            cwd,
		Store:          st,
		Auth:           authStore,
		ApprovalFn:     approvalFn,
		FileApprovalFn: fileApprovalFn,
		Child:          true,
		SandboxManager: childSandboxMgr,
	})

	// Run agent with a nil outputChan (we write events ourselves).
	outputChan := make(chan agent.OutputEvent, 256)
	a := agent.NewAgent(st, prov, reg, cfg, sessionID, outputChan, approvalFn)
	childAgentRef = a
	a.SetModel(childModel)
	if e := os.Getenv("POISSON_SUBAGENT_EFFORT"); e != "" {
		a.SetEffort(e)
	}
	// The parent's /classifier-model pin is instance-wide: without this the
	// child would classify bash risk with its own main model (or the config
	// default), silently ignoring the pin and spending at a different rate
	// than the user asked for.
	if m := os.Getenv("POISSON_SUBAGENT_CLASSIFIER_MODEL"); m != "" {
		a.SetClassifierModel(m)
	}
	// Subagents get the same skill set as the main session (builtin skills
	// plus any user skills under ~/.poisson/skills/), so a child can e.g.
	// invoke code-quality or code-review on its own. Skills that assume
	// spawning further subagents (council, check-work) simply can't use the
	// subagent tool here — recursion is still bounded to one level.
	// noSkills mirrors the parent's own SkillsEnabled() (propagated via
	// SubagentTool → SpawnInput.NoSkills → this --no-skills flag), so a
	// session that disabled skills doesn't have them silently reappear a
	// level down.
	var skillList []skills.Skill
	if !noSkills {
		var err error
		skillList, err = skills.Discover()
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: skills discover: %v\n", err)
		}
	}
	a.SetSkills(!noSkills, skillList)
	// Same provider-gated web tools the parent gets: without this a child had
	// no fetch tool at all (BuildRegistry doesn't register it) and no
	// Anthropic web_search backend even on an Anthropic session.
	a.ReloadConfigDependentTools()

	// Forward the parent's Ctrl+G nudge to the agent, and start the stdin reader
	// now so it listens for expedite even in runs that never hit a bash approval.
	approvalBroker.onExpedite = func() {
		if childAgentRef != nil {
			childAgentRef.Expedite()
		}
	}
	approvalBroker.start()

	var toolCount int
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// This goroutine runs concurrently with the recover() deferred at
		// the top of runChildMode, in a DIFFERENT goroutine — that recover
		// cannot catch a panic here, so it needs its own.
		defer func() {
			if r := recover(); r != nil {
				recoverChildPanic(writeChildEvent, "event-forwarding", r)
			}
		}()
		toolCount = forwardChildEvents(outputChan, a, writeChildEvent)
	}()

	// Subagents run unattended (no per-instance human able to Ctrl+C just
	// this one call), so unlike the interactive main session — which retries
	// network failures indefinitely, relying on the user's own Ctrl+C as the
	// give-up switch — a subagent needs its own bounded retry budget. Past
	// subagentNetworkRetryBudget of continuous retrying it gives up and
	// reports a clear error back to the model, rather than an unattended
	// child silently retrying forever against a network that's simply never
	// coming back.
	retryTrace := &provider.RetryTrace{MaxElapsed: subagentNetworkRetryBudget}
	ctx := provider.WithRetryTrace(context.Background(), retryTrace)

	success := true
	if err := a.PromptWithContext(ctx, task); err != nil {
		writeChildEvent(map[string]interface{}{"type": "error", "error": err.Error()})
		success = false
	}
	close(outputChan)
	wg.Wait()

	ctxUsed, ctxWindow := a.ContextTokens()
	usage := a.CumulativeUsage()
	writeChildEvent(map[string]interface{}{
		"type":          "done",
		"success":       success,
		"toolCount":     toolCount,
		"turns":         a.RunTurns(),
		"contextTokens": ctxUsed,
		"contextWindow": ctxWindow,
		"usage":         usage,
		"cost":          a.CumulativeCost(),
	})
}

// forwardChildEvents drains outputChan, translating each agent.OutputEvent
// into a child JSON event via write (writeChildEvent in production; a plain
// slice-appending fake in tests). Returns the number of OutputToolStart
// events seen, for the final "done" event's toolCount. Extracted out of
// runChildMode so this translation — the entire wire protocol every
// subagent's parent depends on — is testable against a fake outputChan and a
// real *agent.Agent built on a FakeProvider, with no real process, network,
// or LLM call involved.
func forwardChildEvents(outputChan <-chan agent.OutputEvent, a *agent.Agent, write func(map[string]interface{})) int {
	toolCount := 0
	for ev := range outputChan {
		switch ev.Type {
		case agent.OutputText:
			write(map[string]interface{}{"type": "text", "text": ev.Text})
		case agent.OutputToolStart:
			toolCount++
			ctxUsed, ctxWindow := a.ContextTokens()
			usage := a.CumulativeUsage()
			write(map[string]interface{}{
				"type": "tool", "tool": ev.ToolName, "tool_input": ev.ToolInput,
				"turns": a.RunTurns(), "contextTokens": ctxUsed, "contextWindow": ctxWindow,
				"usage": usage, "cost": a.CumulativeCost(),
			})
		case agent.OutputRetrying:
			// Relayed so the parent's subagent widget can show "reconnecting"
			// in place of its turn/context line instead of freezing on stale
			// numbers with no explanation while this child's own network
			// retry (see provider.DoWithRetry) is in progress.
			write(map[string]interface{}{"type": "retrying", "text": ev.Text})
		case agent.OutputInferenceSpeed:
			// Only when there's an actual reading — a zero here would just
			// mean "nothing to report this round" (see
			// agent.OutputInferenceSpeed), and the parent widget already
			// shows nothing until it sees a positive value.
			if ev.TokensPerSec > 0 {
				// outputTokens is the round's weight for the parent's
				// token-weighted running average (see ChildEvent.OutputTokens).
				write(map[string]interface{}{
					"type": "speed", "tokensPerSec": ev.TokensPerSec,
					"outputTokens": ev.OutputTokens,
				})
			}
		case agent.OutputToolResult:
			payload := map[string]interface{}{
				"type":   "tool_result",
				"tool":   ev.ToolName,
				"result": ev.ToolResultContent,
			}
			if ev.ToolError != "" {
				payload["error"] = ev.ToolError
			}
			write(payload)
		}
	}
	return toolCount
}

var childEventMu sync.Mutex

func writeChildEvent(event map[string]interface{}) {
	childEventMu.Lock()
	defer childEventMu.Unlock()
	data, _ := json.Marshal(event)
	fmt.Println(string(data))
}

// recoverChildPanic emits a structured "error" event carrying the recovered
// panic value and a stack trace, for use from a `defer func() { if r :=
// recover(); ... }()` site. label identifies which goroutine panicked (the
// main run vs the event-forwarding goroutine both need their own recover,
// since a panic in one is invisible to a recover() in the other) — without
// this, the parent only ever sees its pipe close and reports a bare "EOF".
// write is a param (not always the package-level writeChildEvent) so this is
// unit-testable without capturing stdout, matching forwardChildEvents' shape.
func recoverChildPanic(write func(map[string]interface{}), label string, r interface{}) {
	write(map[string]interface{}{
		"type":  "error",
		"error": fmt.Sprintf("subagent %s panicked: %v\n%s", label, r, debug.Stack()),
	})
}

// bufioNewScanner wraps bufio.Scanner for stdin reading.
func bufioNewScanner(r interface{ Read([]byte) (int, error) }) *bufioScanner {
	return &bufioScanner{r: r}
}

type bufioScanner struct {
	r   interface{ Read([]byte) (int, error) }
	buf []byte
}

func (s *bufioScanner) Scan() bool {
	line := make([]byte, 0, 1024)
	b := make([]byte, 1)
	for {
		_, err := s.r.Read(b)
		if err != nil {
			if len(line) > 0 {
				s.buf = line
				return true
			}
			return false
		}
		if b[0] == '\n' {
			s.buf = line
			return true
		}
		line = append(line, b[0])
	}
}

func (s *bufioScanner) Bytes() []byte { return s.buf }
