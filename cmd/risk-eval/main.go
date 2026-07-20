// risk-eval runs live LLM evals for the bash command risk classifier.
// Usage: risk-eval --cases evals/bash-risk/cases.json [--mode llm] [--out report.json]
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mq37/poisson/internal/agent"
	"github.com/mq37/poisson/internal/auth"
	"github.com/mq37/poisson/internal/config"
	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/store"
	"github.com/mq37/poisson/internal/tools"
)

type caseDefaults struct {
	Workdir     string `json:"workdir"`
	Description string `json:"description"`
}

type evalCase struct {
	ID              string   `json:"id"`
	Category        string   `json:"category"`
	Tags            []string `json:"tags"`
	Command         string   `json:"command"`
	Description     string   `json:"description"`
	Workdir         string   `json:"workdir"`
	Expect          string   `json:"expect"`
	MustNot         string   `json:"must_not"`
	Retries         int      `json:"retries"`
	Notes           string   `json:"notes"`
	InjectionTarget string   `json:"injection_target"`
}

type caseFile struct {
	Version  int          `json:"version"`
	Defaults caseDefaults `json:"defaults"`
	Cases    []evalCase   `json:"cases"`
}

type caseRun struct {
	ID          string                 `json:"id"`
	Category    string                 `json:"category"`
	Tags        []string               `json:"tags"`
	Command     string                 `json:"command"`
	Description string                 `json:"description"`
	Workdir     string                 `json:"workdir"`
	Expect      string                 `json:"expect"`
	MustNot     string                 `json:"must_not"`
	Got         string                 `json:"got"`
	Source      string                 `json:"source"`
	RawLLM      string                 `json:"raw_llm,omitempty"`
	LLMRuns     []agent.BashRiskLLMRun `json:"llm_runs,omitempty"`
	Pass        bool                   `json:"pass"`
	Critical    bool                   `json:"critical,omitempty"`
	Attempt     int                    `json:"attempt"`
	LatencyMS   int64                  `json:"latency_ms"`
	Notes       string                 `json:"notes,omitempty"`
}

type report struct {
	GeneratedAt string    `json:"generated_at"`
	Provider    string    `json:"provider"`
	Model       string    `json:"model"`
	Mode        string    `json:"mode"`
	Runs        []caseRun `json:"runs"`
	Summary     summary   `json:"summary"`
}

type summary struct {
	Total      int                   `json:"total"`
	Passed     int                   `json:"passed"`
	Failed     int                   `json:"failed"`
	Critical   int                   `json:"critical"`
	ByCategory map[string]catSummary `json:"by_category"`
}

type catSummary struct {
	Total  int `json:"total"`
	Passed int `json:"passed"`
}

func main() {
	casesPath := flag.String("cases", "evals/bash-risk/cases.json", "path to eval cases JSON")
	modeFlag := flag.String("mode", "full", "eval mode: full, llm, guard")
	providerFlag := flag.String("provider", "", "provider id (default from config)")
	modelFlag := flag.String("model", "", "model id (default from config)")
	outPath := flag.String("out", "", "write JSON report to this path")
	summarizePath := flag.String("summarize", "", "print summary for an existing report JSON and exit")
	dryRun := flag.Bool("dry-run", false, "list matching cases without calling the API")
	category := flag.String("category", "", "filter by category")
	tag := flag.String("tag", "", "filter by tag")
	idFilter := flag.String("id", "", "filter by case id prefix")
	delayMS := flag.Int("delay", 300, "delay between cases in milliseconds")
	timeoutSec := flag.Int("timeout", 45, "per-case timeout in seconds (covers dual LLM runs)")
	defaultRetries := flag.Int("retries", 1, "default retry count per case")
	flag.Parse()

	if *summarizePath != "" {
		if err := printSummary(*summarizePath); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		os.Exit(exitCodeFromReport(*summarizePath))
		return
	}

	suite, err := loadCases(*casesPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading cases: %v\n", err)
		os.Exit(1)
	}

	selected := filterCases(suite.Cases, *category, *tag, *idFilter)
	if len(selected) == 0 {
		fmt.Fprintln(os.Stderr, "no cases matched filters")
		os.Exit(1)
	}

	mode := agent.BashRiskEvalMode(strings.ToLower(strings.TrimSpace(*modeFlag)))
	switch mode {
	case agent.BashRiskEvalFull, agent.BashRiskEvalLLM, agent.BashRiskEvalGuard:
	default:
		fmt.Fprintf(os.Stderr, "unknown mode %q (want full, llm, guard)\n", *modeFlag)
		os.Exit(1)
	}

	if *dryRun {
		for _, c := range selected {
			fmt.Printf("%s [%s] expect=%s", c.ID, c.Category, c.Expect)
			if c.MustNot != "" {
				fmt.Printf(" must_not=%s", c.MustNot)
			}
			fmt.Println()
		}
		fmt.Printf("%d case(s)\n", len(selected))
		return
	}

	cfg, err := config.Load()
	if err != nil {
		cfg = config.DefaultConfig()
	}
	authStore, _ := auth.Load()

	provName := strings.TrimSpace(*providerFlag)
	model := strings.TrimSpace(*modelFlag)
	if provName == "" || model == "" {
		bootProv, bootName, bootModel, warn := provider.BootstrapFromConfig(authStore, cfg)
		if warn != "" {
			fmt.Fprintln(os.Stderr, warn)
		}
		if provName == "" {
			provName = bootName
		}
		if model == "" {
			model = bootModel
		}
		if bootProv == nil && mode != agent.BashRiskEvalGuard {
			fmt.Fprintln(os.Stderr, "error: no provider available")
			os.Exit(1)
		}
	}

	var prov provider.Provider
	if mode != agent.BashRiskEvalGuard {
		prov = provider.NewProvider(provName, authStore, cfg)
		if prov == nil {
			fmt.Fprintf(os.Stderr, "error: unknown provider %q\n", provName)
			os.Exit(1)
		}
		if model == "" {
			model = provider.DefaultModel(provName, cfg)
		}
	}

	a, cleanup, err := newEvalAgent(prov, cfg, provName, model)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer cleanup()

	rep := report{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Provider:    provName,
		Model:       model,
		Mode:        string(mode),
		Summary:     summary{ByCategory: map[string]catSummary{}},
	}

	fmt.Printf("bash-risk eval — %s/%s — mode=%s — %d case(s)\n", provName, model, mode, len(selected))

	for i, c := range selected {
		if i > 0 && *delayMS > 0 {
			time.Sleep(time.Duration(*delayMS) * time.Millisecond)
		}
		runs := runCase(a, suite.Defaults, c, mode, *defaultRetries, *timeoutSec)
		for _, r := range runs {
			rep.Runs = append(rep.Runs, r)
			rep.Summary.Total++
			if r.Pass {
				rep.Summary.Passed++
			} else {
				rep.Summary.Failed++
				if r.Critical {
					rep.Summary.Critical++
				}
			}
			cs := rep.Summary.ByCategory[r.Category]
			cs.Total++
			if r.Pass {
				cs.Passed++
			}
			rep.Summary.ByCategory[r.Category] = cs

			status := "PASS"
			if !r.Pass {
				status = "FAIL"
				if r.Critical {
					status = "CRITICAL"
				}
			}
			fmt.Printf("  %s %s expect=%s got=%s source=%s (%dms)\n",
				status, r.ID, r.Expect, r.Got, r.Source, r.LatencyMS)
		}
	}

	if *outPath != "" {
		if err := writeReport(*outPath, rep); err != nil {
			fmt.Fprintf(os.Stderr, "error writing report: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("report: %s\n", *outPath)
	}

	printReportSummary(rep)
	if rep.Summary.Failed > 0 {
		os.Exit(1)
	}
}

func loadCases(path string) (*caseFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var suite caseFile
	if err := json.Unmarshal(data, &suite); err != nil {
		return nil, err
	}
	if suite.Version == 0 {
		suite.Version = 1
	}
	if suite.Defaults.Workdir == "" {
		suite.Defaults.Workdir = "/tmp/poisson-eval"
	}
	return &suite, nil
}

func filterCases(cases []evalCase, category, tag, idPrefix string) []evalCase {
	var out []evalCase
	for _, c := range cases {
		if category != "" && c.Category != category {
			continue
		}
		if idPrefix != "" && !strings.HasPrefix(c.ID, idPrefix) {
			continue
		}
		if tag != "" && !hasTag(c.Tags, tag) {
			continue
		}
		out = append(out, c)
	}
	return out
}

func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}

func newEvalAgent(prov provider.Provider, cfg *config.Config, provName, model string) (*agent.Agent, func(), error) {
	dir, err := os.MkdirTemp("", "poisson-risk-eval-*")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	st, err := store.Open(filepath.Join(dir, "eval.db"))
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	sid := store.NewSessionID()
	cwd, _ := os.Getwd()
	if err := st.CreateSession(&store.Session{
		ID:       sid,
		Cwd:      cwd,
		Provider: provName,
		Model:    model,
	}); err != nil {
		st.Close()
		cleanup()
		return nil, nil, err
	}

	reg := tools.NewRegistry()
	a := agent.NewAgent(st, prov, reg, cfg, sid, nil, nil)
	a.SetModel(model)
	return a, func() {
		st.Close()
		cleanup()
	}, nil
}

func runCase(a *agent.Agent, defaults caseDefaults, c evalCase, mode agent.BashRiskEvalMode, defaultRetries, timeoutSec int) []caseRun {
	retries := c.Retries
	if retries <= 0 {
		retries = defaultRetries
	}
	if retries < 1 {
		retries = 1
	}

	desc := c.Description
	if desc == "" {
		desc = defaults.Description
	}
	workdir := c.Workdir
	if workdir == "" {
		workdir = defaults.Workdir
	}

	var runs []caseRun
	for attempt := 1; attempt <= retries; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
		start := time.Now()
		res := a.AssessBashRiskEval(ctx, c.Command, desc, workdir, mode)
		cancel()

		got := string(res.Risk)
		if got == "" {
			got = "unknown"
		}
		pass := got == c.Expect
		critical := false
		if c.MustNot != "" && got == c.MustNot {
			pass = false
			critical = true
		}

		runs = append(runs, caseRun{
			ID:          c.ID,
			Category:    c.Category,
			Tags:        c.Tags,
			Command:     c.Command,
			Description: desc,
			Workdir:     workdir,
			Expect:      c.Expect,
			MustNot:     c.MustNot,
			Got:         got,
			Source:      string(res.Source),
			RawLLM:      res.RawLLM,
			LLMRuns:     res.LLMRuns,
			Pass:        pass,
			Critical:    critical,
			Attempt:     attempt,
			LatencyMS:   time.Since(start).Milliseconds(),
			Notes:       c.Notes,
		})

		if pass {
			break
		}
	}
	return runs
}

func writeReport(path string, rep report) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func printReportSummary(rep report) {
	fmt.Println("────────────────────────────────────────")
	for cat, cs := range rep.Summary.ByCategory {
		pct := 0
		if cs.Total > 0 {
			pct = cs.Passed * 100 / cs.Total
		}
		fmt.Printf("%-20s %d/%d (%d%%)\n", cat+":", cs.Passed, cs.Total, pct)
	}
	fmt.Println("────────────────────────────────────────")
	fmt.Printf("TOTAL                %d/%d\n", rep.Summary.Passed, rep.Summary.Total)
	if rep.Summary.Critical > 0 {
		fmt.Printf("CRITICAL FAILURES    %d\n", rep.Summary.Critical)
	}
}

func printSummary(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var rep report
	if err := json.Unmarshal(data, &rep); err != nil {
		return err
	}
	fmt.Printf("bash-risk eval — %s/%s — mode=%s\n", rep.Provider, rep.Model, rep.Mode)
	printReportSummary(rep)
	return nil
}

func exitCodeFromReport(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 1
	}
	var rep report
	if err := json.Unmarshal(data, &rep); err != nil {
		return 1
	}
	if rep.Summary.Failed > 0 {
		return 1
	}
	return 0
}
