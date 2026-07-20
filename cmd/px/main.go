package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"poisson/internal/agent"
	"poisson/internal/auth"
	"poisson/internal/config"
	"poisson/internal/provider"
	"poisson/internal/skills"
	"poisson/internal/store"
	"poisson/internal/tools"
	"poisson/internal/tui"
)

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
		runREPL(noSkills)
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
	default:
		fmt.Println("poisson", version)
		fmt.Println("usage: Poisson [command] [options]")
		fmt.Println("  Poisson                     interactive REPL")
		fmt.Println("  Poisson login <provider>    OAuth login")
		fmt.Println("  Poisson logout <provider>   clear stored tokens")
		fmt.Println("  Poisson sessions            list sessions")
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
	humanApproval := func(command, description, workdir string, risk agent.BashRisk) (bool, string) {
		return yolo, "" // headless: only --yolo approves escalated commands
	}
	approvalFn := func(ctx context.Context, command, description, workdir string) (bool, string) {
		if agentRef != nil {
			return agent.WrapRiskGatedApproval(agentRef, humanApproval)(ctx, command, description, workdir)
		}
		return humanApproval(command, description, workdir, agent.BashRiskUnknown)
	}
	// Sensitive files (.env*, SSH/cloud credentials, ~/.poisson secrets, ...)
	// are deterministically flagged by guard.SensitivePathReason, so this asks
	// the human directly — no LLM risk classification needed.
	fileApprovalFn := func(ctx context.Context, action, reason, workdir string) (bool, string) {
		return humanApproval(action, reason, workdir, agent.BashRiskHigh)
	}
	reg := tools.BuildRegistry(tools.BuildOptions{Cwd: cwd, Store: st, Auth: authStore, ApprovalFn: approvalFn, FileApprovalFn: fileApprovalFn})

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

// runREPL starts the interactive REPL.
func runREPL(noSkills bool) {
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
	humanApproval := func(command, description, workdir string, risk agent.BashRisk) (bool, string) {
		if approveUI != nil {
			return approveUI.Approve(command, description, workdir, risk)
		}
		return false, ""
	}
	approvalFn := func(ctx context.Context, command, description, workdir string) (bool, string) {
		if agentRef != nil {
			return agent.WrapRiskGatedApproval(agentRef, humanApproval)(ctx, command, description, workdir)
		}
		return humanApproval(command, description, workdir, agent.BashRiskUnknown)
	}
	// Sensitive files (.env*, SSH/cloud credentials, ~/.poisson secrets, ...)
	// are deterministically flagged by guard.SensitivePathReason, so this asks
	// the human directly — no LLM risk classification needed.
	fileApprovalFn := func(ctx context.Context, action, reason, workdir string) (bool, string) {
		return humanApproval(action, reason, workdir, agent.BashRiskHigh)
	}

	subApprovalFn := func(command, description, workdir, agentName, risk string) (bool, string) {
		_ = agentName // overlay uses command context; name available for future UI
		return humanApproval(command, description, workdir, agent.ParseBashRisk(risk))
	}

	reg := tools.BuildRegistry(tools.BuildOptions{
		Cwd:            cwd,
		Store:          st,
		Auth:           authStore,
		ApprovalFn:     approvalFn,
		FileApprovalFn: fileApprovalFn,
		SubApproval:    subApprovalFn,
	})

	// Set up agent.
	a := agent.NewAgent(st, prov, reg, cfg, sessionID, outputChan, approvalFn)
	agentRef = a
	tools.BindSubagentRuntime(reg, func() string { return a.Provider().ID() }, func() string { return a.Model() }, func() string { return a.Effort() })
	tools.BindSubagentProgress(reg, a.SendSubagentProgress)

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
	approveUI = t
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
		short := s.ID
		if len(short) > 8 {
			short = short[:8]
		}
		fmt.Printf("  %s  %s  %d msgs  %s/%s\n", short, date, len(msgs), s.Provider, s.Model)
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
		fmt.Printf("Session %s:\n", sid)
		if breakdown.InputUnknownCalls > 0 {
			fmt.Printf("  Input:  %d tokens + unknown (%d call(s))\n", breakdown.InputTokens, breakdown.InputUnknownCalls)
		} else {
			fmt.Printf("  Input:  %d tokens\n", breakdown.InputTokens)
		}
		fmt.Printf("  Output: %d tokens\n", breakdown.OutputTokens)
		fmt.Printf("  Calls:  %d\n", breakdown.CallCount)
		fmt.Printf("  Cost:   $%.4f\n", cost)
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
	// Parse args: --json --no-skills --session <id> [-- task]
	var sessionID, task string
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
		case "--no-skills":
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
	if childModel == "" {
		switch childProv {
		case "anthropic":
			childModel = cfg.Anthropic.Model
		case "xai":
			childModel = cfg.XAI.Model
		default:
			childProv = "ollama"
			childModel = cfg.Ollama.Model
		}
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
		switch childProv {
		case "anthropic":
			prov = provider.NewAnthropicProvider(authStore, cfg)
		case "xai":
			prov = provider.NewXAIProvider(authStore, cfg)
		default:
			prov = provider.NewOllamaProvider(cfg.Ollama.BaseURL, childModel)
		}
	}

	sandbox := os.Getenv("POISSON_SANDBOX") == "1"
	var approvalBroker childApprovalBroker

	var childAgentRef *agent.Agent
	humanChildApproval := func(command, description, workdir string, risk agent.BashRisk) (bool, string) {
		if sandbox {
			return true, ""
		}
		return approvalBroker.emitAndWait(map[string]interface{}{
			"type":        "approval_request",
			"command":     command,
			"description": description,
			"cwd":         workdir,
			"risk":        string(risk),
			"agent":       os.Getenv("POISSON_SUBAGENT_NAME"),
		})
	}
	approvalFn := func(ctx context.Context, command, description, workdir string) (bool, string) {
		if sandbox {
			return true, ""
		}
		if childAgentRef != nil {
			return agent.WrapRiskGatedApproval(childAgentRef, humanChildApproval)(ctx, command, description, workdir)
		}
		return humanChildApproval(command, description, workdir, agent.BashRiskUnknown)
	}
	fileApprovalFn := func(ctx context.Context, action, reason, workdir string) (bool, string) {
		if sandbox {
			return true, ""
		}
		return humanChildApproval(action, reason, workdir, agent.BashRiskHigh)
	}

	// Child:true grants every tool except subagent, so a subagent gets the full
	// tool set (read/write/edit/bash/search/ls/glob/web_search/web_ask/recall)
	// but cannot spawn further subagents — recursion is bounded to one level.
	reg := tools.BuildRegistry(tools.BuildOptions{
		Cwd:            cwd,
		Store:          st,
		Auth:           authStore,
		Sandbox:        sandbox,
		ApprovalFn:     approvalFn,
		FileApprovalFn: fileApprovalFn,
		Child:          true,
	})

	// Run agent with a nil outputChan (we write events ourselves).
	outputChan := make(chan agent.OutputEvent, 256)
	a := agent.NewAgent(st, prov, reg, cfg, sessionID, outputChan, approvalFn)
	childAgentRef = a
	a.SetModel(childModel)
	if e := os.Getenv("POISSON_SUBAGENT_EFFORT"); e != "" {
		a.SetEffort(e)
	}
	a.SetSkills(false, nil)

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
	writeChildEvent(map[string]interface{}{
		"type":          "done",
		"success":       success,
		"toolCount":     toolCount,
		"turns":         a.RunTurns(),
		"contextTokens": ctxUsed,
		"contextWindow": ctxWindow,
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
			write(map[string]interface{}{
				"type": "tool", "tool": ev.ToolName, "tool_input": ev.ToolInput,
				"turns": a.RunTurns(), "contextTokens": ctxUsed, "contextWindow": ctxWindow,
			})
		case agent.OutputRetrying:
			// Relayed so the parent's subagent widget can show "reconnecting"
			// in place of its turn/context line instead of freezing on stale
			// numbers with no explanation while this child's own network
			// retry (see provider.DoWithRetry) is in progress.
			write(map[string]interface{}{"type": "retrying", "text": ev.Text})
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
