package main

import (
	"encoding/json"
	"fmt"
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
	var cmdArgs []string
	for _, a := range os.Args[1:] {
		if a == "--no-skills" {
			noSkills = true
			continue
		}
		cmdArgs = append(cmdArgs, a)
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
	humanApproval := func(command, description, workdir string, risk agent.BashRisk) bool {
		if approveUI != nil {
			return approveUI.Approve(command, description, workdir, risk)
		}
		return false
	}
	approvalFn := func(command, description, workdir string) bool {
		if agentRef != nil {
			return agent.WrapRiskGatedApproval(agentRef, humanApproval)(command, description, workdir)
		}
		return humanApproval(command, description, workdir, agent.BashRiskUnknown)
	}

	subOutputFn := func(eventType, text, toolName string, toolInput json.RawMessage, toolErr string) {
		switch eventType {
		case "text":
			outputChan <- agent.OutputEvent{Type: agent.OutputText, Text: text}
		case "tool_start":
			outputChan <- agent.OutputEvent{Type: agent.OutputToolStart, ToolName: toolName, ToolInput: toolInput}
		case "tool_result":
			outputChan <- agent.OutputEvent{
				Type:              agent.OutputToolResult,
				ToolName:          toolName,
				ToolResultContent: text,
				ToolError:         toolErr,
			}
		}
	}
	subApprovalFn := func(command, description, workdir, agentName, risk string) bool {
		_ = agentName // overlay uses command context; name available for future UI
		return humanApproval(command, description, workdir, agent.ParseBashRisk(risk))
	}

	reg := tools.BuildRegistry(tools.BuildOptions{
		Cwd:         cwd,
		Store:       st,
		ApprovalFn:  approvalFn,
		SubOutput:   subOutputFn,
		SubApproval: subApprovalFn,
	})

	// Set up agent.
	a := agent.NewAgent(st, prov, reg, cfg, sessionID, outputChan, approvalFn)
	agentRef = a
	tools.BindSubagentRuntime(reg, func() string { return a.Provider().ID() }, func() string { return a.Model() }, func() string { return a.Effort() })

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
		fmt.Println("providers: anthropic, xai")
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
func runChildMode() {
	// Parse args: --json --no-skills --session <id> --tools <list> [-- task]
	var sessionID, task, toolsList string
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
		case "--tools":
			if i+1 < len(args) {
				toolsList = args[i+1]
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
	dbPath := filepath.Join(config.ConfigDir(), "poisson.db")
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
	humanChildApproval := func(command, description, workdir string, risk agent.BashRisk) bool {
		if sandbox {
			return true
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
	approvalFn := func(command, description, workdir string) bool {
		if sandbox {
			return true
		}
		if childAgentRef != nil {
			return agent.WrapRiskGatedApproval(childAgentRef, humanChildApproval)(command, description, workdir)
		}
		return humanChildApproval(command, description, workdir, agent.BashRiskUnknown)
	}

	reg := tools.BuildRegistry(tools.BuildOptions{
		Cwd:        cwd,
		Sandbox:    sandbox,
		ApprovalFn: approvalFn,
		Tools:      toolsList,
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

	var toolCount int
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for ev := range outputChan {
			switch ev.Type {
			case agent.OutputText:
				writeChildEvent(map[string]interface{}{"type": "text", "text": ev.Text})
			case agent.OutputToolStart:
				toolCount++
				writeChildEvent(map[string]interface{}{"type": "tool", "tool": ev.ToolName, "tool_input": ev.ToolInput})
			case agent.OutputToolResult:
				payload := map[string]interface{}{
					"type":   "tool_result",
					"tool":   ev.ToolName,
					"result": ev.ToolResultContent,
				}
				if ev.ToolError != "" {
					payload["error"] = ev.ToolError
				}
				writeChildEvent(payload)
			}
		}
	}()

	success := true
	if err := a.Prompt(task); err != nil {
		writeChildEvent(map[string]interface{}{"type": "error", "error": err.Error()})
		success = false
	}
	close(outputChan)
	wg.Wait()

	ctxUsed, _ := a.ContextTokens()
	writeChildEvent(map[string]interface{}{
		"type":          "done",
		"success":       success,
		"toolCount":     toolCount,
		"turns":         1,
		"contextTokens": ctxUsed,
	})
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
