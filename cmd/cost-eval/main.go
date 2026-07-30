// cost-eval measures token usage and cost per model for two workloads, so a
// subscription burning fast on one model can be traced to the layer paying for
// it rather than guessed at.
//
//	convo — one plain question, no tools: baseline main-loop spend.
//	bash  — five gated read-only commands: every command also pays for one LLM
//	        bash-risk classification, which is where a pricey model hurts most.
//
// Each run drives a real agent turn against the live provider in an isolated
// temp DB, then reads spend back out of api_calls grouped by purpose
// (main | risk | compaction | subagent | btw), so the classifier's share is
// separated from the conversation's.
//
// Usage:
//
//	go run ./cmd/cost-eval
//	go run ./cmd/cost-eval --reps 3 --case bash
//	go run ./cmd/cost-eval --main claude-opus-5 --classifier claude-sonnet-5
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mq37/poisson/internal/agent"
	"github.com/mq37/poisson/internal/auth"
	"github.com/mq37/poisson/internal/config"
	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/skills"
	"github.com/mq37/poisson/internal/store"
	"github.com/mq37/poisson/internal/tools"
)

const convoPrompt = "In two sentences and with no tool calls, explain the difference between TCP and UDP."

// bashPrompt names commands that are deliberately absent from the guard's
// safe list (awk, nl, sort, printf, go), so each one reaches the LLM risk
// classifier instead of being auto-approved deterministically. They are all
// read-only, so a "low" verdict auto-runs them and the turn proceeds.
const bashPrompt = `Run each of these five commands with the bash tool, one bash call per command, in this order, and do NOT use the batch tool:
1. awk "END{print NR}" go.mod
2. nl -ba build.sh | tail -3
3. sort go.sum | cut -c1-24 | uniq | wc -l
4. printf "%s\n" hello-bench
5. go version
When all five are done, reply with exactly: DONE`

// scenario is one workload.
type scenario struct {
	name   string
	prompt string
}

var scenarios = []scenario{
	{"convo", convoPrompt},
	{"bash", bashPrompt},
}

// combo is one (main model, classifier model) pairing to measure. An empty
// classifier means "inherit the main model", which is poisson's default and
// the configuration that makes an expensive main model pay twice.
type combo struct {
	main       string
	classifier string
}

// spend is per-purpose usage read back from api_calls.
type spend struct {
	purpose    string
	calls      int
	input      int
	output     int
	cacheRead  int
	cacheWrite int
	cost       float64
}

func (s spend) tokens() int { return s.input + s.output + s.cacheRead + s.cacheWrite }

// result is one scenario × combo × rep.
type result struct {
	scenario   string
	main       string
	classifier string
	rep        int
	wall       time.Duration
	spends     []spend
	err        error
}

func main() {
	var (
		providerID  = flag.String("provider", "anthropic", "provider id")
		mainModels  = flag.String("main", "claude-sonnet-5,claude-opus-5", "comma-separated main models")
		classifiers = flag.String("classifier", "", "classifier model for the pricey main model (default: also measure inherit-only)")
		only        = flag.String("case", "", "run only this scenario (convo|bash)")
		reps        = flag.Int("reps", 1, "repetitions per combination")
		noSkills    = flag.Bool("no-skills", false, "omit skills from the system prompt")
		timeout     = flag.Duration("timeout", 5*time.Minute, "per-run timeout")
	)
	flag.Parse()

	cfg, err := config.Load()
	if err != nil || cfg == nil {
		cfg = config.DefaultConfig()
	}
	authStore, _ := auth.Load()
	prov := provider.NewProvider(*providerID, authStore, cfg)
	if prov == nil {
		fmt.Fprintf(os.Stderr, "unknown provider %q\n", *providerID)
		os.Exit(2)
	}

	combos := buildCombos(strings.Split(*mainModels, ","), splitNonEmpty(*classifiers))
	if len(combos) == 0 {
		fmt.Fprintln(os.Stderr, "no models to measure")
		os.Exit(2)
	}

	var skillList []skills.Skill
	if !*noSkills {
		skillList, _ = skills.Discover()
	}

	cwd, _ := os.Getwd()
	dbDir, err := os.MkdirTemp("", "px-cost-eval-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "temp dir: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dbDir)

	var results []result
	for rep := 1; rep <= *reps; rep++ {
		for _, sc := range scenarios {
			if *only != "" && *only != sc.name {
				continue
			}
			for _, cb := range combos {
				fmt.Fprintf(os.Stderr, "== %s  main=%s  classifier=%s  rep=%d\n",
					sc.name, cb.main, classifierLabel(cb.classifier), rep)
				r := run(runInput{
					dbDir:     dbDir,
					cwd:       cwd,
					cfg:       cfg,
					prov:      prov,
					authStore: authStore,
					skills:    skillList,
					useSkills: !*noSkills,
					timeout:   *timeout,
					scenario:  sc,
					combo:     cb,
					rep:       rep,
				})
				if r.err != nil {
					fmt.Fprintf(os.Stderr, "   run error: %v\n", r.err)
				}
				results = append(results, r)
			}
		}
	}

	report(results)
}

// buildCombos pairs every main model with "inherit", plus every explicit
// classifier override — the three-way comparison the report needs (cheap
// model, pricey model, pricey model with a cheap classifier).
func buildCombos(mains, classifiers []string) []combo {
	var out []combo
	for _, m := range mains {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		out = append(out, combo{main: m})
		for _, c := range classifiers {
			if c != m {
				out = append(out, combo{main: m, classifier: c})
			}
		}
	}
	return out
}

func splitNonEmpty(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func classifierLabel(c string) string {
	if c == "" {
		return "(inherit)"
	}
	return c
}

type runInput struct {
	dbDir     string
	cwd       string
	cfg       *config.Config
	prov      provider.Provider
	authStore auth.AuthStore
	skills    []skills.Skill
	useSkills bool
	timeout   time.Duration
	scenario  scenario
	combo     combo
	rep       int
}

// run drives one agent turn and returns its per-purpose spend. Every run gets
// its own store so api_calls rows can't bleed between measurements.
func run(in runInput) result {
	res := result{
		scenario: in.scenario.name, main: in.combo.main,
		classifier: in.combo.classifier, rep: in.rep,
	}

	dbName := fmt.Sprintf("%s-%s-%s-r%d.db", in.scenario.name, in.combo.main, classifierLabel(in.combo.classifier), in.rep)
	st, err := store.Open(filepath.Join(in.dbDir, strings.ReplaceAll(dbName, "/", "_")))
	if err != nil {
		res.err = fmt.Errorf("open store: %w", err)
		return res
	}
	defer st.Close()

	sessionID := store.NewSessionID()
	if err := st.CreateSession(&store.Session{
		ID: sessionID, Cwd: in.cwd, Provider: in.prov.ID(), Model: in.combo.main, CreatedAt: time.Now().Unix(),
	}); err != nil {
		res.err = fmt.Errorf("create session: %w", err)
		return res
	}

	// Headless: no human is here to approve, so anything the classifier does
	// not rate "low" is denied. The classification call still happens, which
	// is exactly the spend being measured.
	var agentRef *agent.Agent
	deny := func(ctx context.Context, command, description, workdir string, risk agent.BashRisk, origin agent.ApprovalOrigin) (bool, string) {
		return false, ""
	}
	approvalFn := func(ctx context.Context, command, description, workdir string) (bool, string) {
		if agentRef == nil {
			return false, ""
		}
		return agent.WrapRiskGatedApproval(agentRef, deny)(ctx, command, description, workdir)
	}
	reg := tools.BuildRegistry(tools.BuildOptions{
		Cwd: in.cwd, Store: st, Auth: in.authStore,
		ApprovalFn:     approvalFn,
		FileApprovalFn: func(ctx context.Context, action, reason, workdir string) (bool, string) { return false, "" },
	})

	outputChan := make(chan agent.OutputEvent, 256)
	a := agent.NewAgent(st, in.prov, reg, in.cfg, sessionID, outputChan, approvalFn)
	agentRef = a
	if err := a.SetModel(in.combo.main); err != nil {
		res.err = fmt.Errorf("set model: %w", err)
		return res
	}
	// The knob under test: pinning the classifier is per-agent, so no config
	// file has to be rewritten between runs.
	a.SetClassifierModel(in.combo.classifier)
	a.SetSkills(in.useSkills, in.skills)
	a.ReloadConfigDependentTools()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range outputChan {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), in.timeout)
	defer cancel()
	start := time.Now()
	promptErr := a.PromptWithContext(ctx, in.scenario.prompt)
	res.wall = time.Since(start)
	close(outputChan)
	<-done
	if promptErr != nil {
		res.err = promptErr
	}

	spends, err := readSpend(st, sessionID)
	if err != nil && res.err == nil {
		res.err = err
	}
	res.spends = spends
	return res
}

// readSpend aggregates a session's api_calls rows by purpose.
func readSpend(st *store.Store, sessionID string) ([]spend, error) {
	rows, err := st.DB().Query(
		`SELECT purpose, COUNT(*), SUM(input_tokens), SUM(output_tokens),
		        SUM(cache_read_tokens), SUM(cache_write_tokens), SUM(cost)
		 FROM api_calls WHERE session_id = ? GROUP BY purpose ORDER BY purpose`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query api_calls: %w", err)
	}
	defer rows.Close()

	var out []spend
	for rows.Next() {
		var s spend
		if err := rows.Scan(&s.purpose, &s.calls, &s.input, &s.output, &s.cacheRead, &s.cacheWrite, &s.cost); err != nil {
			return nil, fmt.Errorf("scan api_calls: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// report prints one row per purpose per run, a total per run, and the
// classifier-share summary that answers "where is the quota going".
func report(results []result) {
	fmt.Printf("\n%-6s %-16s %-16s %-11s %5s %6s %9s %8s %11s %11s %11s\n",
		"case", "main model", "classifier", "purpose", "rep", "calls", "input", "output", "cache_read", "cache_write", "cost_usd")
	for _, r := range results {
		var total spend
		for _, s := range r.spends {
			fmt.Printf("%-6s %-16s %-16s %-11s %5d %6d %9d %8d %11d %11d %11.4f\n",
				r.scenario, r.main, classifierLabel(r.classifier), s.purpose, r.rep,
				s.calls, s.input, s.output, s.cacheRead, s.cacheWrite, s.cost)
			total.calls += s.calls
			total.input += s.input
			total.output += s.output
			total.cacheRead += s.cacheRead
			total.cacheWrite += s.cacheWrite
			total.cost += s.cost
		}
		fmt.Printf("%-6s %-16s %-16s %-11s %5d %6d %9d %8d %11d %11d %11.4f  (%.1fs)\n\n",
			r.scenario, r.main, classifierLabel(r.classifier), "TOTAL", r.rep,
			total.calls, total.input, total.output, total.cacheRead, total.cacheWrite,
			total.cost, r.wall.Seconds())
	}

	type agg struct {
		runs               int
		tokens, riskTokens int
		cost, riskCost     float64
		riskCalls          int
	}
	sums := map[string]*agg{}
	var order []string
	for _, r := range results {
		key := fmt.Sprintf("%s|%s|%s", r.scenario, r.main, classifierLabel(r.classifier))
		a, ok := sums[key]
		if !ok {
			a = &agg{}
			sums[key] = a
			order = append(order, key)
		}
		a.runs++
		for _, s := range r.spends {
			a.tokens += s.tokens()
			a.cost += s.cost
			if s.purpose == "risk" {
				a.riskTokens += s.tokens()
				a.riskCost += s.cost
				a.riskCalls += s.calls
			}
		}
	}
	sort.Strings(order)

	fmt.Printf("%-6s %-16s %-16s %5s %10s %11s %11s %9s %13s\n",
		"case", "main model", "classifier", "runs", "tokens", "cost_usd", "risk_cost", "risk_%", "per_risk_call")
	for _, key := range order {
		a := sums[key]
		parts := strings.SplitN(key, "|", 3)
		share := 0.0
		if a.cost > 0 {
			share = 100 * a.riskCost / a.cost
		}
		perCall := 0.0
		if a.riskCalls > 0 {
			perCall = a.riskCost / float64(a.riskCalls)
		}
		fmt.Printf("%-6s %-16s %-16s %5d %10d %11.4f %11.4f %8.1f%% %13.5f\n",
			parts[0], parts[1], parts[2], a.runs, a.tokens/a.runs, a.cost/float64(a.runs),
			a.riskCost/float64(a.runs), share, perCall)
	}
}
