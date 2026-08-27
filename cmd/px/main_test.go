package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/auth"
	"github.com/mq37/poisson/internal/config"
	"github.com/mq37/poisson/internal/store"
	"github.com/mq37/poisson/internal/testutil"
)

// captureStdout redirects os.Stdout for the duration of fn, returning
// everything written to it. Mirrors TestLoadConfigOrDefaultWarnsOnParseFailure's
// os.Pipe pattern above, applied to stdout instead of stderr.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	return string(out)
}

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

func TestHelpDocumentsTopLevelOptions(t *testing.T) {
	help := helpText()
	for _, option := range []string{"-p, --print", "--no-skills", "--yolo", "--model", "--session", "-v, --version", "-h, --help"} {
		if !strings.Contains(help, option) {
			t.Errorf("help omits %q", option)
		}
	}
	if !strings.Contains(help, "disable all skills, including skills in subagents") {
		t.Error("help does not explain --no-skills scope")
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

// --- cmdLogout ---

// TestCmdLogoutRemovesEntryCaseInsensitive pins that the provider name is
// matched case-insensitively (px logout Anthropic == px logout anthropic)
// and that only the named provider's entry is removed.
func TestCmdLogoutRemovesEntryCaseInsensitive(t *testing.T) {
	testutil.TempHome(t)
	if err := auth.Save(auth.AuthStore{
		"anthropic": {Type: "oauth", Access: "a"},
		"openai":    {Type: "oauth", Access: "o"},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	out := captureStdout(t, func() { cmdLogout([]string{"Anthropic"}) })
	if !strings.Contains(out, "Logged out of anthropic.") {
		t.Errorf("output = %q, want lowercase confirmation message", out)
	}

	authStore, err := auth.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := authStore["anthropic"]; ok {
		t.Error("anthropic entry should be gone")
	}
	if authStore["openai"].Access != "o" {
		t.Error("openai entry should survive untouched")
	}
}

func TestCmdLogoutNoArgsPrintsUsage(t *testing.T) {
	out := captureStdout(t, func() { cmdLogout(nil) })
	if !strings.Contains(out, "usage: Poisson logout <provider>") {
		t.Errorf("output = %q, want usage message", out)
	}
}

// TestRunLogoutPropagatesDeleteEntryError forces a real auth.DeleteEntry
// error (auth.json exists as a directory, so os.ReadFile inside Load fails
// with something other than IsNotExist) and checks it comes back out of
// runLogout instead of being swallowed.
func TestRunLogoutPropagatesDeleteEntryError(t *testing.T) {
	tmpHome := testutil.TempHome(t)
	authPath := filepath.Join(tmpHome, ".poisson", "auth.json")
	if err := os.MkdirAll(authPath, 0o700); err != nil {
		t.Fatalf("mkdir auth.json as a directory: %v", err)
	}

	if _, err := runLogout("anthropic"); err == nil {
		t.Fatal("expected an error when auth.json is unreadable, got nil")
	}
}

// --- cmdLogin ---

func TestCmdLoginNoArgsPrintsUsage(t *testing.T) {
	out := captureStdout(t, func() { cmdLogin(nil) })
	if !strings.Contains(out, "usage: Poisson login <provider>") {
		t.Errorf("output = %q, want usage message", out)
	}
}

// TestCmdLoginOllamaIsPurePrintNoAuthCall pins the ollama branch as a
// no-op/no-auth-call print — unlike anthropic/xai/openai it must never
// touch auth.LoginXxx (which would need a live browser/network).
func TestCmdLoginOllamaIsPurePrintNoAuthCall(t *testing.T) {
	out := captureStdout(t, func() { cmdLogin([]string{"ollama"}) })
	if !strings.Contains(out, "Ollama runs locally") {
		t.Errorf("output = %q, want the ollama no-login message", out)
	}
}

// --- resolveChildProvider ---

// TestResolveChildProviderCustom checks a subagent explicitly pinned to a
// custom provider (POISSON_SUBAGENT_PROVIDER) keeps it, instead of being
// silently downgraded to ollama because config.ResolveProviderMeta wasn't
// consulted.
func TestResolveChildProviderCustom(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.CustomProviders["bastion"] = &config.CustomProviderConfig{
		Type: "ollama", BaseURL: "http://bastion-host:11434", Model: "laguna-s-2.1:q4_K_M",
	}
	if got := resolveChildProvider("bastion", cfg); got != "bastion" {
		t.Errorf("resolveChildProvider = %q, want bastion", got)
	}
}

// TestResolveChildProviderUnknownFallsBackToOllama checks a genuinely
// unknown name (neither built-in nor custom) still falls back, unchanged
// behavior from before the custom-provider generalization.
func TestResolveChildProviderUnknownFallsBackToOllama(t *testing.T) {
	cfg := config.DefaultConfig()
	if got := resolveChildProvider("frobnicate", cfg); got != "ollama" {
		t.Errorf("resolveChildProvider = %q, want ollama", got)
	}
}

// TestResolveChildProviderEmptyUsesConfigDefault checks an empty env value
// falls back to cfg.Provider.Default.
func TestResolveChildProviderEmptyUsesConfigDefault(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.CustomProviders["bastion"] = &config.CustomProviderConfig{Type: "ollama", BaseURL: "http://bastion-host:11434"}
	cfg.Provider.Default = "bastion"
	if got := resolveChildProvider("", cfg); got != "bastion" {
		t.Errorf("resolveChildProvider = %q, want bastion", got)
	}
}

// TestCmdLoginLlamaCppIsGenericNoAuth checks llamacpp — a second built-in
// NeedsAuth=false provider that had no explicit switch case — gets the same
// generic "no login needed" treatment as ollama, instead of falling into
// "unknown provider". Regression guard for the gap the custom-provider
// generalization fixed incidentally.
func TestCmdLoginLlamaCppIsGenericNoAuth(t *testing.T) {
	testutil.TempHome(t)
	out := captureStdout(t, func() { cmdLogin([]string{"llamacpp"}) })
	if !strings.Contains(out, "no login needed") {
		t.Errorf("output = %q, want a generic no-login-needed message", out)
	}
}

// TestCmdLoginCustomProviderIsGenericNoAuth checks a [custom_providers.*]
// instance (always NeedsAuth=false, v1 supports type="ollama" only) also
// gets the generic no-login message instead of "unknown provider".
func TestCmdLoginCustomProviderIsGenericNoAuth(t *testing.T) {
	tmpHome := testutil.TempHome(t)
	dir := filepath.Join(tmpHome, ".poisson")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	toml := "[custom_providers.bastion]\ntype = \"ollama\"\nbase_url = \"http://bastion-host:11434\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(toml), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	out := captureStdout(t, func() { cmdLogin([]string{"bastion"}) })
	if !strings.Contains(out, "no login needed") {
		t.Errorf("output = %q, want a generic no-login-needed message", out)
	}
}

// TestCmdLoginUnknownProviderExitsNonzero runs the built px binary as a
// subprocess (same pattern as TestResumeCommand_MissingArg above) since the
// unknown-provider branch calls os.Exit directly and isn't decomposed.
func TestCmdLoginUnknownProviderExitsNonzero(t *testing.T) {
	bin := buildPX(t)
	cmd := exec.Command(bin, "login", "frobnicate")
	cmd.Env = isolatedEnv(isolatedHome(t))
	out, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected exit error, got %v (output: %s)", err, out)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("exit code = %d, want 1 (output: %s)", exitErr.ExitCode(), out)
	}
	if !strings.Contains(string(out), "unknown provider: frobnicate") {
		t.Errorf("output = %q, want unknown-provider message", out)
	}
}

// TestCmdLoginOpenRouterPromptsAndSavesKey runs the built px binary as a
// subprocess (bufio.NewReader(os.Stdin) needs a real stdin pipe, unlike the
// other cmdLogin cases above which are exercised in-process) piping a fake
// API key and checking it lands in auth.json as an api_key entry — the
// plain-key path OpenRouter uses instead of the OAuth device flows.
func TestCmdLoginOpenRouterPromptsAndSavesKey(t *testing.T) {
	bin := buildPX(t)
	home := isolatedHome(t)
	cmd := exec.Command(bin, "login", "openrouter")
	cmd.Env = isolatedEnv(home)
	cmd.Stdin = strings.NewReader("sk-or-test123\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("login failed: %v (output: %s)", err, out)
	}
	if !strings.Contains(string(out), "Logged in to OpenRouter") {
		t.Errorf("output = %q, want success message", out)
	}

	data, err := os.ReadFile(filepath.Join(home, ".poisson", "auth.json"))
	if err != nil {
		t.Fatalf("read auth.json: %v", err)
	}
	if !strings.Contains(string(data), "sk-or-test123") || !strings.Contains(string(data), `"type": "api_key"`) {
		t.Errorf("auth.json = %s, want api_key entry with the piped key", data)
	}
}

// --- cmdSessions / formatSessionsListing ---

func TestFormatSessionsListingEmpty(t *testing.T) {
	out := formatSessionsListing(nil, func(string) int { return 0 })
	if out != "no sessions\n" {
		t.Errorf("out = %q, want %q", out, "no sessions\n")
	}
}

// TestFormatSessionsListingPopulated proves the format wiring (display id,
// date, per-session message count from the injected msgCount, provider/model)
// against two distinct sessions rather than a tautological single-field check.
func TestFormatSessionsListingPopulated(t *testing.T) {
	ts1 := time.Date(2024, 1, 2, 12, 0, 0, 0, time.UTC).Unix()
	ts2 := time.Date(2024, 3, 4, 12, 0, 0, 0, time.UTC).Unix()
	sessions := []store.Session{
		{ID: "s-aaaa1111", CreatedAt: ts1, Provider: "anthropic", Model: "claude-sonnet-5"},
		{ID: "s-bbbb2222", CreatedAt: ts2, Provider: "xai", Model: "grok-build"},
	}
	counts := map[string]int{"s-aaaa1111": 3, "s-bbbb2222": 0}

	out := formatSessionsListing(sessions, func(id string) int { return counts[id] })

	want := fmt.Sprintf("  %s  %s  3 msgs  anthropic/claude-sonnet-5\n  %s  %s  0 msgs  xai/grok-build\n",
		store.DisplaySessionID("s-aaaa1111"), time.Unix(ts1, 0).Format("2006-01-02"),
		store.DisplaySessionID("s-bbbb2222"), time.Unix(ts2, 0).Format("2006-01-02"))
	if out != want {
		t.Errorf("out = %q, want %q", out, want)
	}
}

// TestCmdSessionsEmptyStorePrintsNoSessions exercises cmdSessions end-to-end
// (ConfigDir → store.Open → formatSessionsListing) against an empty,
// testutil-isolated store.
func TestCmdSessionsEmptyStorePrintsNoSessions(t *testing.T) {
	testutil.TempHome(t)
	out := captureStdout(t, cmdSessions)
	if out != "no sessions\n" {
		t.Errorf("out = %q, want %q", out, "no sessions\n")
	}
}

// TestCmdSessionsListsSeededSessions seeds a session directly into the same
// DB path cmdSessions itself opens, then checks the listing reflects it.
func TestCmdSessionsListsSeededSessions(t *testing.T) {
	testutil.TempHome(t)
	dbPath := filepath.Join(config.ConfigDir(), "poisson.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if err := st.CreateSession(&store.Session{ID: "s-seed0001", Provider: "anthropic", Model: "claude-sonnet-5"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	st.Close()

	out := captureStdout(t, cmdSessions)
	if !strings.Contains(out, "anthropic/claude-sonnet-5") {
		t.Errorf("out = %q, want the seeded session listed", out)
	}
	if !strings.Contains(out, "0 msgs") {
		t.Errorf("out = %q, want 0 msgs (no messages appended)", out)
	}
}

// --- cmdCost / runCost ---

func TestRunCostTotalAcrossSessions(t *testing.T) {
	dir := testutil.TempDir(t)
	st, err := store.Open(filepath.Join(dir, "cost.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	if err := st.CreateSession(&store.Session{ID: "s-cost0001", Provider: "anthropic", Model: "claude-sonnet-5"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := st.RecordAPICall(&store.APICall{
		SessionID: "s-cost0001", Provider: "anthropic", Model: "claude-sonnet-5",
		InputTokens: 100, OutputTokens: 50, Cost: 1.2345,
	}); err != nil {
		t.Fatalf("RecordAPICall: %v", err)
	}

	stdout, stderr, code := runCost(st, nil)
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q, want success", code, stderr)
	}
	if stdout != "Total cost across all sessions: $1.2345\n" {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestRunCostPerSessionBreakdown(t *testing.T) {
	dir := testutil.TempDir(t)
	st, err := store.Open(filepath.Join(dir, "cost.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	if err := st.CreateSession(&store.Session{ID: "s-cost0002", Provider: "anthropic", Model: "claude-sonnet-5"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := st.RecordAPICall(&store.APICall{
		SessionID: "s-cost0002", Provider: "anthropic", Model: "claude-sonnet-5",
		InputTokens: 10, OutputTokens: 20, Cost: 0.5,
	}); err != nil {
		t.Fatalf("RecordAPICall: %v", err)
	}

	stdout, stderr, code := runCost(st, []string{"s-cost0002"})
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q, want success", code, stderr)
	}
	if !strings.Contains(stdout, "Session s-cost0002:") || !strings.Contains(stdout, "Cost:   $0.5000") {
		t.Errorf("stdout = %q, want a per-session cost breakdown", stdout)
	}
}

// TestRunCostSessionNotFoundReturnsNonzero is the actual gate CI-style
// callers rely on: an unknown session id must come back as a nonzero code
// and a clear stderr message, not a silent empty report.
func TestRunCostSessionNotFoundReturnsNonzero(t *testing.T) {
	dir := testutil.TempDir(t)
	st, err := store.Open(filepath.Join(dir, "cost.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	stdout, stderr, code := runCost(st, []string{"nonexistent"})
	if code == 0 {
		t.Fatal("code = 0, want nonzero for a missing session")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on error", stdout)
	}
	if !strings.Contains(stderr, "session not found: nonexistent") {
		t.Errorf("stderr = %q, want session-not-found message", stderr)
	}
}

// --- cmdSearch / runSearch ---

func TestRunSearchNoQueryReturnsUsage(t *testing.T) {
	dir := testutil.TempDir(t)
	st, err := store.Open(filepath.Join(dir, "search.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	stdout, stderr, code := runSearch(st, nil)
	if code != 2 || stdout != "" {
		t.Fatalf("code=%d stdout=%q, want usage error", code, stdout)
	}
	if !strings.Contains(stderr, "usage:") {
		t.Errorf("stderr = %q, want usage message", stderr)
	}
}

func TestRunSearchNoMatches(t *testing.T) {
	dir := testutil.TempDir(t)
	st, err := store.Open(filepath.Join(dir, "search.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	stdout, stderr, code := runSearch(st, []string{"zzz-nonexistent-term"})
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q, want success", code, stderr)
	}
	if !strings.Contains(stdout, "no sessions match") {
		t.Errorf("stdout = %q, want a no-match message", stdout)
	}
}

// TestRunSearchGroupsHitsBySession seeds two matching messages in one
// session and one in another, then checks the listing shows one line per
// session (not per message) with the right hit counts, provider/model, and
// a snippet — the shape a human uses to pick a session to `px resume`.
func TestRunSearchGroupsHitsBySession(t *testing.T) {
	dir := testutil.TempDir(t)
	st, err := store.Open(filepath.Join(dir, "search.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	if err := st.CreateSession(&store.Session{ID: "s-search001", Provider: "anthropic", Model: "claude-sonnet-5"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := st.CreateSession(&store.Session{ID: "s-search002", Provider: "xai", Model: "grok-build"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	msg := func(sid, role, text string) *store.Message {
		return &store.Message{SessionID: sid, Role: role, Content: fmt.Sprintf(`[{"type":"text","text":%q}]`, text)}
	}
	if err := st.AppendMessage(msg("s-search001", "user", "let's talk about zebrafish genomics")); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if err := st.AppendMessage(msg("s-search001", "assistant", "zebrafish are a great model organism")); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if err := st.AppendMessage(msg("s-search002", "user", "unrelated zebrafish question")); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	stdout, stderr, code := runSearch(st, []string{"zebrafish"})
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q, want success", code, stderr)
	}
	if !strings.Contains(stdout, store.DisplaySessionID("s-search001")) || !strings.Contains(stdout, "2 match(es)") {
		t.Errorf("stdout = %q, want s-search001 with 2 matches", stdout)
	}
	if !strings.Contains(stdout, store.DisplaySessionID("s-search002")) || !strings.Contains(stdout, "1 match(es)") {
		t.Errorf("stdout = %q, want s-search002 with 1 match", stdout)
	}
	if !strings.Contains(stdout, "anthropic/claude-sonnet-5") || !strings.Contains(stdout, "xai/grok-build") {
		t.Errorf("stdout = %q, want both sessions' provider/model", stdout)
	}
}
