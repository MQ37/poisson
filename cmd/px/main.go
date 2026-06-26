package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

	if len(os.Args) < 2 {
		runREPL()
		return
	}

	switch os.Args[1] {
	case "login":
		cmdLogin(os.Args[2:])
	case "logout":
		cmdLogout(os.Args[2:])
	case "-v", "--version", "version":
		fmt.Println("poisson", version)
	case "sessions":
		cmdSessions()
	case "cost":
		cmdCost(os.Args[2:])
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
func runREPL() {
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
	st.SeedPricing()

	// Load auth.
	authStore, _ := auth.Load()

	// Determine provider — fall back to ollama if anthropic has no auth.
	provName := cfg.Provider.Default
	if provName == "" {
		provName = "ollama"
	}
	if provName == "anthropic" && !auth.IsOAuth(authStore, "anthropic") && auth.GetAPIKey(authStore, "anthropic") == "" && cfg.Anthropic.APIKey == "" {
		// No anthropic credentials configured — fall back to ollama.
		provName = "ollama"
		fmt.Fprintln(os.Stderr, "no anthropic credentials found, using ollama")
	}

	var prov provider.Provider
	var model string
	switch provName {
	case "anthropic":
		prov = provider.NewAnthropicProvider(authStore, cfg)
		model = cfg.Anthropic.Model
		if model == "" {
			model = "claude-opus-4-8"
		}
	case "xai":
		prov = provider.NewXAIProvider(authStore, cfg)
		model = cfg.XAI.Model
		if model == "" {
			model = "grok-build"
		}
	default:
		prov = provider.NewOllamaProvider(cfg.Ollama.BaseURL, cfg.Ollama.Model)
		model = cfg.Ollama.Model
		if model == "" {
			model = "glm-5.2:cloud"
		}
	}

	// Create or resume session.
	sessionID := store.NewSessionID()
	cwd, _ := os.Getwd()

	// Print startup banner.
	fmt.Printf("Poisson %s | %s/%s\n", version, provName, model)
	fmt.Printf("session: %s\n", sessionID)
	fmt.Println()

	if err := st.CreateSession(&store.Session{
		ID:        sessionID,
		Cwd:       cwd,
		Provider:  provName,
		Model:     model,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}); err != nil {
		fmt.Fprintf(os.Stderr, "error creating session: %v\n", err)
		os.Exit(1)
	}

	// Set up tools.
	reg := tools.NewRegistry()
	// Approval callback for dangerous bash commands. It delegates to the TUI
	// (set below) so the prompt owns stdin exclusively in blocking mode; the
	// terminal runs raw with a nonblocking Ctrl+C poller otherwise.
	var approveUI tui.Approver
	approvalFn := func(command, description, workdir string) bool {
		if approveUI != nil {
			return approveUI.Approve(command, description)
		}
		return false
	}

	reg.Register(tools.NewBashTool(cwd, false, approvalFn))
	reg.Register(tools.NewReadTool(cwd))
	reg.Register(tools.NewWriteTool(cwd))
	reg.Register(tools.NewEditTool(cwd))
	reg.Register(tools.NewSearchTool(cwd))
	reg.Register(tools.NewLsTool(cwd))
	reg.Register(tools.NewGlobTool(cwd))
	reg.Register(tools.NewRecallTool(st))

	// Set up output channel.
	outputChan := make(chan agent.OutputEvent, 256)

	// Subagent tool (only in parent mode).
	subOutputFn := func(eventType, text, toolName string, toolInput json.RawMessage) {
		switch eventType {
		case "text":
			outputChan <- agent.OutputEvent{Type: agent.OutputText, Text: text}
		case "tool_start":
			outputChan <- agent.OutputEvent{Type: agent.OutputToolStart, ToolName: toolName, ToolInput: toolInput}
		}
	}
	subApprovalFn := func(command, description, workdir, agentName string) bool {
		return approvalFn(command, description, workdir)
	}
	reg.Register(tools.NewSubagentTool(cwd, st, subOutputFn, subApprovalFn))

	// Skills.
	skillList, _ := skills.Discover()
	if len(skillList) > 0 {
		reg.Register(tools.NewSkillTool(skillList))
	}

	// Network tools.
	reg.Register(tools.NewExaSearchTool())
	if tools.IsOllamaReachable(cfg) {
		reg.Register(tools.NewFetchTool(cfg.Ollama.BaseURL))
	}

	// Set up agent.
	a := agent.NewAgent(st, prov, reg, cfg, sessionID, outputChan, approvalFn)

	// Run TUI.
	t := tui.NewTUI(a, sessionID, outputChan)
	runErr := t.Run(func(a tui.Approver) { approveUI = a })
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
		cost, _ := st.GetSessionCost(sid)
		breakdown, _ := st.GetSessionTokenBreakdown(sid)
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

	cost, _ := st.GetTotalCost()
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
	// Parse args: --json --no-skills --session <id> --tools <list> [task]
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
		default:
			if !strings.HasPrefix(args[i], "-") && task == "" {
				task = args[i]
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
	st.SeedPricing()

	// Create or use existing session.
	if sessionID == "" {
		sessionID = store.NewSubagentID()
	}
	cwd, _ := os.Getwd()
	st.CreateSession(&store.Session{
		ID:         sessionID,
		Cwd:        cwd,
		Provider:   "ollama",
		Model:      cfg.Ollama.Model,
		IsSubagent: true,
		CreatedAt:  time.Now().Unix(),
		UpdatedAt:  time.Now().Unix(),
	})

	// Set up provider (always ollama in child mode).
	prov := provider.NewOllamaProvider(cfg.Ollama.BaseURL, cfg.Ollama.Model)

	sandbox := os.Getenv("POISSON_SANDBOX") == "1"

	// Approval: write to stdout, read from stdin. Must be defined before tool
	// registration so bash gets the callback (not nil).
	approvalFn := func(command, description, workdir string) bool {
		if sandbox {
			return true
		}
		writeChildEvent(map[string]interface{}{
			"type":        "approval_request",
			"command":     command,
			"description": description,
			"cwd":         workdir,
			"agent":       os.Getenv("POISSON_SUBAGENT_NAME"),
		})
		scanner := bufioNewScanner(os.Stdin)
		if scanner.Scan() {
			var resp struct {
				Type     string `json:"type"`
				Approved bool   `json:"approved"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &resp); err == nil {
				return resp.Approved
			}
		}
		return false
	}

	// Set up tools — restricted set.
	reg := tools.NewRegistry()
	for _, name := range strings.Split(toolsList, ",") {
		name = strings.TrimSpace(name)
		switch name {
		case "read":
			reg.Register(tools.NewReadTool(cwd))
		case "write":
			reg.Register(tools.NewWriteTool(cwd))
		case "edit":
			reg.Register(tools.NewEditTool(cwd))
		case "bash":
			reg.Register(tools.NewBashTool(cwd, sandbox, approvalFn))
		case "search":
			reg.Register(tools.NewSearchTool(cwd))
		case "ls":
			reg.Register(tools.NewLsTool(cwd))
		case "glob":
			reg.Register(tools.NewGlobTool(cwd))
		}
	}

	// Run agent with a nil outputChan (we write events ourselves).
	outputChan := make(chan agent.OutputEvent, 256)
	a := agent.NewAgent(st, prov, reg, cfg, sessionID, outputChan, approvalFn)

	// Drain outputChan and write to stdout as JSON lines.
	go func() {
		for ev := range outputChan {
			switch ev.Type {
			case agent.OutputText:
				writeChildEvent(map[string]interface{}{"type": "text", "text": ev.Text})
			case agent.OutputToolStart:
				writeChildEvent(map[string]interface{}{"type": "tool", "tool": ev.ToolName, "tool_input": ev.ToolInput})
			}
		}
	}()

	// Run the prompt.
	if err := a.Prompt(task); err != nil {
		writeChildEvent(map[string]interface{}{"type": "error", "error": err.Error()})
	}

	// Write done event.
	writeChildEvent(map[string]interface{}{
		"type":    "done",
		"success": true,
	})
}

func writeChildEvent(event map[string]interface{}) {
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
