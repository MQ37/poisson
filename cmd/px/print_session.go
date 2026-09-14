package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mq37/poisson/internal/agent"
	"github.com/mq37/poisson/internal/auth"
	"github.com/mq37/poisson/internal/config"
	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/sandbox"
	"github.com/mq37/poisson/internal/skills"
	"github.com/mq37/poisson/internal/store"
	"github.com/mq37/poisson/internal/tools"
)

// printSetupError carries an exit code alongside a message, so both
// runPrint (plain text) and runPrintJSON can report a buildPrintAgent
// failure through their own separate output channel (stderr text vs a JSON
// error+done event) while still exiting with the exact same code the
// pre-refactor inline checks used (2 for a bad model/provider, 1 for
// everything else).
type printSetupError struct {
	code int
	msg  string
}

func (e *printSetupError) Error() string { return e.msg }

// printSetupExitCode extracts the intended exit code from a buildPrintAgent
// error, defaulting to 1 for anything that isn't a *printSetupError (there
// shouldn't be any, but this is cheaper than a second failure mode).
func printSetupExitCode(err error) int {
	var se *printSetupError
	if errors.As(err, &se) {
		return se.code
	}
	return 1
}

// printAgentBuild bundles everything runPrint and runPrintJSON share once
// setup succeeds: the constructed agent, its store (closed via cleanup),
// the output channel it writes agent.OutputEvent to, and — only when
// jsonOut and not --yolo — the approval broker reading responses off stdin.
type printAgentBuild struct {
	agent   *agent.Agent
	output  chan agent.OutputEvent
	broker  *childApprovalBroker // nil in text mode or --yolo (nothing asks)
	cleanup func()
}

// buildPrintAgent does every piece of setup runPrint and runPrintJSON both
// need: config/store/session-resolve, approval closures, tools.BuildRegistry,
// agent.NewAgent, skills, and ReloadConfigDependentTools. Extracted out of
// the old runPrint (which did all this inline) so JSON mode doesn't
// duplicate it — see docs/orchestrator-plan.md Step 6. Deliberately does NOT
// pull in runREPL's TUI-specific wiring (approveUI, sudoUI, ...); that would
// drag the whole TUI into this feature's blast radius for zero benefit here.
func buildPrintAgent(opts printOpts) (*printAgentBuild, error) {
	cfg := loadConfigOrDefault()
	dbPath := filepath.Join(config.ConfigDir(), "poisson.db")
	st, err := store.Open(dbPath)
	if err != nil {
		return nil, &printSetupError{1, fmt.Sprintf("error opening database: %v", err)}
	}

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
		st.Close()
		return nil, &printSetupError{1, fmt.Sprintf("error reading session: %v", err)}
	}

	provName, model, err := resolvePrintRuntime(opts.model, sess, cfg)
	if err != nil {
		st.Close()
		return nil, &printSetupError{2, fmt.Sprintf("px -p: %v", err)}
	}
	prov := provider.NewProvider(provName, authStore, cfg)
	if prov == nil {
		st.Close()
		return nil, &printSetupError{2, fmt.Sprintf("px -p: unknown provider %q (want %s, or a [custom_providers.*] name)",
			provName, strings.Join(config.ProviderIDs(), "/"))}
	}

	if sess == nil {
		if err := st.CreateSession(&store.Session{
			ID: sessionID, Cwd: cwd, Provider: provName, Model: model, CreatedAt: time.Now().Unix(),
		}); err != nil {
			st.Close()
			return nil, &printSetupError{1, fmt.Sprintf("error creating session: %v", err)}
		}
	} else if sess.Provider != provName || sess.Model != model {
		// Persist the pair in one UPDATE. Writing provider and model
		// separately can leave an impossible combination if the second
		// write fails.
		sess.Provider, sess.Model = provName, model
		if err := st.UpdateSession(sess); err != nil {
			st.Close()
			return nil, &printSetupError{1, fmt.Sprintf("error updating session model: %v", err)}
		}
	}

	yolo := opts.yolo
	var agentRef *agent.Agent
	var broker *childApprovalBroker
	var humanApproval func(ctx context.Context, command, description, workdir string, risk agent.BashRisk, origin agent.ApprovalOrigin) (bool, string)
	if opts.jsonOut && !yolo {
		// JSON mode with real approvals: round-trip the request over
		// stdout/stdin via the same broker + wire shape runChildMode's
		// subagents already use (childApprovalBroker, approveViaChildBroker)
		// — see docs/orchestrator-plan.md Step 8. No "agent" field: that's
		// child-mode-specific metadata the parent uses to label a subagent's
		// approval, not relevant to a top-level print session.
		broker = &childApprovalBroker{}
		humanApproval = func(ctx context.Context, command, description, workdir string, risk agent.BashRisk, origin agent.ApprovalOrigin) (bool, string) {
			event := map[string]interface{}{
				"type":        "approval_request",
				"command":     command,
				"description": description,
				"cwd":         workdir,
				"risk":        string(risk),
			}
			if agentRef != nil {
				event["usage"] = agentRef.CumulativeUsage()
				event["cost"] = agentRef.CumulativeCost()
			}
			return approveViaChildBroker(ctx, broker, event)
		}
	} else {
		humanApproval = func(ctx context.Context, command, description, workdir string, risk agent.BashRisk, origin agent.ApprovalOrigin) (bool, string) {
			return yolo, "" // headless, no broker: only --yolo approves escalated commands
		}
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

	var sandboxMgr *sandbox.Manager
	if !opts.jsonOut {
		// podman isn't in the orchestrator's nspawn golden image, so
		// offering create_sandbox to a --print-json instance would just
		// produce confusing tool failures for no benefit — see
		// docs/orchestrator-plan.md Step 6.
		sandboxMgr = newSandboxManager(sessionID)
	}

	reg := tools.BuildRegistry(tools.BuildOptions{
		Cwd:               cwd,
		Store:             st,
		Auth:              authStore,
		ApprovalFn:        approvalFn,
		FileApprovalFn:    fileApprovalFn,
		SandboxManager:    sandboxMgr,
		SandboxApprovalFn: sandboxApprovalFn,
		// Set by whichever orchestrator Runtime launched this turn
		// (nspawn/hostrt's --setenv=POISSON_ORCHESTRATE_INSTANCE=1) -- an
		// orchestrator instance has no TUI window for set_title to rename.
		// A human running px -p --print-json directly never sets this.
		NoSetTitle: os.Getenv("POISSON_ORCHESTRATE_INSTANCE") == "1",
	})

	outputChan := make(chan agent.OutputEvent, 256)
	a := agent.NewAgent(st, prov, reg, cfg, sessionID, outputChan, approvalFn)
	agentRef = a
	tools.BindSessionTitle(reg, a.SessionID, a.EnsureSession)
	if err := a.SetModel(model); err != nil {
		st.Close()
		return nil, &printSetupError{1, fmt.Sprintf("error updating session model: %v", err)}
	}
	var skillList []skills.Skill
	if !opts.noSkills {
		skillList, _ = skills.Discover()
	}
	a.SetSkills(!opts.noSkills, skillList)
	a.ReloadConfigDependentTools()

	if broker != nil {
		broker.onExpedite = func() {
			if agentRef != nil {
				agentRef.Expedite()
			}
		}
	}

	return &printAgentBuild{
		agent:   a,
		output:  outputChan,
		broker:  broker,
		cleanup: func() { st.Close() },
	}, nil
}

// printRunContext returns a context cancelled on SIGINT/SIGTERM, and a stop
// func to release the signal handler. If broker is non-nil, the signal also
// triggers broker.denyAllWaiters() directly — cancelling ctx alone can't
// unblock a goroutine parked on the broker's plain (non-context-aware)
// channel wait (see docs/orchestrator-plan.md Step 9). A second signal
// forces an immediate hard exit rather than waiting on a graceful shutdown
// that might itself be stuck. Shared by runPrint and runPrintJSON so plain
// -p also gets a clean exit on Ctrl-C instead of relying on Go's bare
// default signal handling — deliberate, not a JSON-mode-only change.
func printRunContext(broker *childApprovalBroker) (context.Context, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		if _, ok := <-sigCh; !ok {
			return
		}
		cancel()
		if broker != nil {
			broker.denyAllWaiters()
		}
		if _, ok := <-sigCh; ok {
			os.Exit(1)
		}
	}()
	return ctx, func() {
		signal.Stop(sigCh)
		close(sigCh)
	}
}

// runPrintJSON runs a single prompt headlessly like runPrint, but emits a
// JSON event stream on stdout (one object per line — see forwardChildEvents)
// instead of plain text, and reads bash-approval responses back from stdin
// via a childApprovalBroker unless --yolo. This is the wire protocol the
// orchestrator's nspawn Runtime (M2+) drives every instance turn through —
// see docs/orchestrator-plan.md Steps 7-9.
func runPrintJSON(opts printOpts) {
	// A panic anywhere below would otherwise crash this process bare, the
	// same reasoning recoverChildPanic's doc comment gives for runChildMode:
	// the parent only ever sees its stdout pipe close with zero diagnostic
	// value.
	defer func() {
		if r := recover(); r != nil {
			recoverChildPanic(writeChildEvent, "run", r)
			os.Exit(1)
		}
	}()

	build, err := buildPrintAgent(opts)
	if err != nil {
		writeChildEvent(map[string]interface{}{"type": "error", "error": err.Error()})
		writeChildEvent(map[string]interface{}{"type": "done", "success": false, "error": err.Error()})
		os.Exit(printSetupExitCode(err))
	}
	defer build.cleanup()
	a := build.agent

	ctx, stop := printRunContext(build.broker)
	defer stop()

	if build.broker != nil {
		build.broker.start()
	}

	var toolCount int
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Runs concurrently with the recover() deferred at the top of this
		// function, in a DIFFERENT goroutine — that recover cannot catch a
		// panic here, so it needs its own (same reasoning as runChildMode).
		defer func() {
			if r := recover(); r != nil {
				recoverChildPanic(writeChildEvent, "event-forwarding", r)
			}
		}()
		toolCount = forwardChildEvents(build.output, a, writeChildEvent)
	}()

	success := true
	errMsg := ""
	if err := a.PromptWithContext(ctx, opts.prompt); err != nil {
		success = false
		if errors.Is(ctx.Err(), context.Canceled) {
			errMsg = "terminated"
		} else {
			errMsg = err.Error()
		}
		writeChildEvent(map[string]interface{}{"type": "error", "error": errMsg})
	}
	close(build.output)
	wg.Wait()

	ctxUsed, ctxWindow := a.ContextTokens()
	doneEvent := map[string]interface{}{
		"type":          "done",
		"success":       success,
		"toolCount":     toolCount,
		"turns":         a.RunTurns(),
		"contextTokens": ctxUsed,
		"contextWindow": ctxWindow,
		"usage":         a.CumulativeUsage(),
		"cost":          a.CumulativeCost(),
	}
	if errMsg != "" {
		doneEvent["error"] = errMsg
	}
	writeChildEvent(doneEvent)
	if !success {
		os.Exit(1)
	}
}
