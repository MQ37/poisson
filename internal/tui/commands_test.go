package tui

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/agent"
	"github.com/mq37/poisson/internal/auth"
	"github.com/mq37/poisson/internal/config"
	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/store"
	"github.com/mq37/poisson/internal/testutil"
	"github.com/mq37/poisson/internal/tools"
)

// --- Test helpers for session commands ---

func newTestStoreAndAgent(t *testing.T) (*store.Store, *agent.Agent, string) {
	t.Helper()
	testutil.TempHome(t)
	dir := testutil.TempDir(t)
	dbPath := filepath.Join(dir, "test.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	sessionID := "test-cmd-session"
	cfg := config.DefaultConfig()
	if err := s.CreateSession(&store.Session{
		ID:        sessionID,
		Cwd:       ".",
		Provider:  "ollama",
		Model:     cfg.Ollama.Model,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	prov := provider.NewFakeProvider("ollama", []provider.Model{{ID: cfg.Ollama.Model, ContextWindow: 8192}})
	reg := tools.NewRegistry()
	a := agent.NewAgent(s, prov, reg, cfg, sessionID, make(chan agent.OutputEvent, 64), func(context.Context, string, string, string) (bool, string) { return false, "" })
	return s, a, sessionID
}

func newTUIWithAgent(a *agent.Agent, sessionID string) *TUI {
	t := NewTUI(a, sessionID, make(chan agent.OutputEvent, 64))
	t.rows = 24
	t.cols = 80
	t.scrollRows = 20
	t.writer = &bytes.Buffer{}
	return t
}

func cmdHost(tui *TUI) commandHost { return tuiCmdHost{tui} }

func testScrollOutput(tui *TUI) string {
	var parts []string
	for i := 0; i < tui.scroll.blockCount(); i++ {
		parts = append(parts, tui.scroll.blockRaw(i))
	}
	return strings.Join(parts, "\n")
}

// --- /new ---

func TestCmdNew(t *testing.T) {
	s, a, originalID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, originalID)

	if err := cmdNew(cmdHost(tui)); err != nil {
		t.Fatalf("cmdNew: %v", err)
	}
	out := testScrollOutput(tui)
	if !strings.Contains(out, "new session") {
		t.Errorf("expected 'new session', got %q", out)
	}
	if tui.sessionID == originalID {
		t.Error("sessionID should have changed")
	}
	if _, err := s.GetSession(tui.sessionID); err == nil {
		t.Error("new session should not be persisted until the first message")
	}
}

func TestCmdNewResetsToolCounts(t *testing.T) {
	_, a, originalID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, originalID)
	// Leftover counts from the previous session's status events.
	tui.status.ToolCalls = 7
	tui.status.ToolErrors = 3

	if err := cmdNew(cmdHost(tui)); err != nil {
		t.Fatalf("cmdNew: %v", err)
	}
	if tui.status.ToolCalls != 0 || tui.status.ToolErrors != 0 {
		t.Fatalf("tool counts not reset on /new: %dT/%de", tui.status.ToolCalls, tui.status.ToolErrors)
	}
}

// --- /name ---

func TestCmdName(t *testing.T) {
	s, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	if err := cmdName(cmdHost(tui), nil); err != nil {
		t.Fatalf("cmdName show unset: %v", err)
	}
	if out := testScrollOutput(tui); !strings.Contains(out, "title: (unset)") {
		t.Errorf("expected unset title, got %q", out)
	}

	if err := cmdName(cmdHost(tui), []string{"Poisson", "experiments"}); err != nil {
		t.Fatalf("cmdName set: %v", err)
	}
	if out := testScrollOutput(tui); !strings.Contains(out, "session title: Poisson experiments") {
		t.Errorf("expected set confirmation, got %q", out)
	}
	got, err := s.GetSession(sessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.Title == nil || *got.Title != "Poisson experiments" {
		t.Fatalf("title = %v, want %q", got.Title, "Poisson experiments")
	}

	tui.scroll = newScrollback(1024)
	if err := cmdName(cmdHost(tui), nil); err != nil {
		t.Fatalf("cmdName show set: %v", err)
	}
	if out := testScrollOutput(tui); !strings.Contains(out, "title: Poisson experiments") {
		t.Errorf("expected saved title, got %q", out)
	}
}

// --- /resume ---

func TestCmdResume(t *testing.T) {
	s, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	otherID := "other-session"
	s.CreateSession(&store.Session{
		ID: otherID, Cwd: ".", Provider: "ollama", Model: a.Config().Ollama.Model,
		CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(),
	})
	tui.scroll.appendRaw(styleAssistant, "stale-marker from previous session")

	if err := cmdResume(cmdHost(tui), []string{otherID}); err != nil {
		t.Fatalf("cmdResume: %v", err)
	}
	if tui.sessionID != otherID {
		t.Errorf("expected session %q, got %q", otherID, tui.sessionID)
	}
	if out := testScrollOutput(tui); strings.Contains(out, "stale-marker") {
		t.Errorf("resume should clear old scrollback, got %q", out)
	}
	if out := testScrollOutput(tui); !strings.Contains(out, "resumed session") {
		t.Errorf("expected resume message, got %q", out)
	}
}

func TestCmdResumeRestoresProviderAndModel(t *testing.T) {
	s, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	otherID := "xai-session"
	if err := s.CreateSession(&store.Session{
		ID: otherID, Cwd: ".", Provider: "xai", Model: a.Config().XAI.Model,
		CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("create xai session: %v", err)
	}

	cmdResume(cmdHost(tui), []string{otherID})
	if tui.sessionID != otherID {
		t.Fatalf("expected session %q, got %q", otherID, tui.sessionID)
	}
	if got := a.Provider().ID(); got != "xai" {
		t.Fatalf("provider = %q, want xai", got)
	}
	if got := a.Model(); got != a.Config().XAI.Model {
		t.Fatalf("model = %q, want %q", got, a.Config().XAI.Model)
	}
}

func TestCmdResumeNotFound(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	cmdResume(cmdHost(tui), []string{"nonexistent"})
	if out := testScrollOutput(tui); !strings.Contains(out, "session not found") {
		t.Errorf("expected not found, got %q", out)
	}
}

func TestCmdResumeNoArg(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	cmdResume(cmdHost(tui), nil)
	if out := testScrollOutput(tui); !strings.Contains(out, "usage") {
		t.Errorf("expected usage, got %q", out)
	}
}

// --- /search ---

func TestCmdSearch(t *testing.T) {
	s, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	s.AppendMessage(&store.Message{
		SessionID: sessionID,
		Role:      "user",
		Content:   `{"text":"hello world"}`,
	})

	cmdSearch(cmdHost(tui), []string{"hello"})
	out := testScrollOutput(tui)
	if !strings.Contains(out, "[hello]") {
		t.Errorf("expected search result with highlighted term, got %q", out)
	}
}

func TestCmdSearchNoResults(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	cmdSearch(cmdHost(tui), []string{"nonexistent"})
	if out := testScrollOutput(tui); !strings.Contains(out, "no results") {
		t.Errorf("expected no results, got %q", out)
	}
}

func TestCmdSearchNoQuery(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	cmdSearch(cmdHost(tui), nil)
	if out := testScrollOutput(tui); !strings.Contains(out, "usage") {
		t.Errorf("expected usage, got %q", out)
	}
}

// --- /model ---

func TestCmdModel(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	cmdModel(cmdHost(tui), []string{"ollama/test-model-2"})
	if a.Model() != "test-model-2" {
		t.Errorf("expected model test-model-2, got %q", a.Model())
	}
	if out := testScrollOutput(tui); !strings.Contains(out, "test-model-2") {
		t.Errorf("expected model message, got %q", out)
	}
}

// configureAuth writes an OAuth entry for each provider into the (temp-HOME)
// auth store so cmdModel's configured-provider gate lets the switch through.
func configureAuth(t *testing.T, ids ...string) {
	t.Helper()
	st := auth.AuthStore{}
	for _, id := range ids {
		st[id] = auth.AuthEntry{Type: "oauth", Access: "test-token"}
	}
	if err := auth.Save(st); err != nil {
		t.Fatalf("save auth: %v", err)
	}
}

func TestCmdModelWithProvider(t *testing.T) {
	s, a, sessionID := newTestStoreAndAgent(t)
	configureAuth(t, "xai")
	tui := newTUIWithAgent(a, sessionID)

	cmdModel(cmdHost(tui), []string{"xai/grok-build"})
	if a.Provider().ID() != "xai" {
		t.Errorf("expected provider xai, got %q", a.Provider().ID())
	}
	if a.Model() != "grok-build" {
		t.Errorf("expected model grok-build, got %q", a.Model())
	}
	sess, err := s.GetSession(sessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if sess.Provider != "xai" || sess.Model != "grok-build" {
		t.Fatalf("session metadata = %s/%s, want xai/grok-build", sess.Provider, sess.Model)
	}
}

// An unconfigured provider must not switch (it would report success then fail
// at request time). The switch is refused with a helpful message.
func TestCmdModelRefusesUnconfiguredProvider(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t) // temp HOME → xai unconfigured
	tui := newTUIWithAgent(a, sessionID)
	before := a.Provider().ID()

	cmdModel(cmdHost(tui), []string{"xai/grok-build"})
	if a.Provider().ID() != before {
		t.Fatalf("provider switched to %q despite xai being unconfigured", a.Provider().ID())
	}
	if out := testScrollOutput(tui); !strings.Contains(out, "not configured") {
		t.Errorf("expected 'not configured' message, got %q", out)
	}
}

func TestCmdModelUpdatesContextWindow(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	configureAuth(t, "xai")
	tui := newTUIWithAgent(a, sessionID)
	tui.status.ContextWindow = 12345 // stale value from a previous model

	cmdModel(cmdHost(tui), []string{"xai/grok-build"})

	ms, ok := provider.GetModelSettings("xai", "grok-build")
	if !ok {
		t.Fatal("missing model settings for xai/grok-build")
	}
	if tui.status.ContextWindow != ms.ContextWindow {
		t.Fatalf("ContextWindow = %d after switch, want %d", tui.status.ContextWindow, ms.ContextWindow)
	}
}

func TestCmdModelProviderOnlyResetsToDefault(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	cmdModel(cmdHost(tui), []string{"ollama/custom"})
	cmdModel(cmdHost(tui), []string{"ollama"})
	if a.Model() != a.Config().Ollama.Model {
		t.Fatalf("model = %q, want default %q", a.Model(), a.Config().Ollama.Model)
	}
	if out := testScrollOutput(tui); !strings.Contains(out, "model: ollama/") {
		t.Fatalf("expected model output, got %q", out)
	}
}

func TestCmdModelRejectsEmptyModel(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	cmdModel(cmdHost(tui), []string{"ollama/"})
	if out := testScrollOutput(tui); !strings.Contains(out, "usage") {
		t.Fatalf("expected usage, got %q", out)
	}
}

func TestCmdModelNoArg(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	cmdModel(cmdHost(tui), nil)
	if out := testScrollOutput(tui); !strings.Contains(out, "current") {
		t.Errorf("expected current model message, got %q", out)
	}
}

// --- /cost ---

func TestCmdCost(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	cmdCost(cmdHost(tui))
	out := testScrollOutput(tui)
	if !strings.Contains(out, "tokens") {
		t.Errorf("expected token output, got %q", out)
	}
}

func TestCmdCostEmpty(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	cmdCost(cmdHost(tui))
	out := testScrollOutput(tui)
	if !strings.Contains(out, "Cost") && !strings.Contains(out, "calls") {
		t.Errorf("expected cost output, got %q", out)
	}
}

func TestCmdCostEphemeralSession(t *testing.T) {
	testutil.TempHome(t)
	dir := testutil.TempDir(t)
	s, err := store.Open(filepath.Join(dir, "ephemeral.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	sessionID := store.NewSessionID()
	cfg := config.DefaultConfig()
	prov := provider.NewFakeProvider("ollama", []provider.Model{{ID: cfg.Ollama.Model, ContextWindow: 8192}})
	a := agent.NewAgent(s, prov, tools.NewRegistry(), cfg, sessionID, make(chan agent.OutputEvent, 64), func(context.Context, string, string, string) (bool, string) { return false, "" })
	tui := newTUIWithAgent(a, sessionID)

	cmdCost(cmdHost(tui))
	out := testScrollOutput(tui)
	if !strings.Contains(out, "not saved yet") {
		t.Errorf("expected ephemeral hint, got %q", out)
	}
}

// --- /reload ---

func TestCmdReload(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	cmdReload(cmdHost(tui))
	if out := testScrollOutput(tui); !strings.Contains(out, "reloaded") {
		t.Errorf("expected reload message, got %q", out)
	}
	if _, ok := a.Provider().(*provider.FakeProvider); ok {
		t.Fatal("provider was not rebuilt after reload")
	}
}

// --- /compact ---

func TestCmdCompactStub(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	if out := testScrollOutput(tui); out != "" {
		t.Logf("scroll output before compact: %q", out)
	}
}

// --- /effort ---

func TestCmdEffort(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	cmdEffort(cmdHost(tui), []string{"high"})
	if a.Effort() != "high" {
		t.Errorf("expected effort high, got %q", a.Effort())
	}
	if out := testScrollOutput(tui); !strings.Contains(out, "high") {
		t.Errorf("expected effort message, got %q", out)
	}
}

func TestCmdEffortInvalid(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	cmdEffort(cmdHost(tui), []string{"bogus"})
	if out := testScrollOutput(tui); !strings.Contains(out, "unknown") {
		t.Errorf("expected unknown effort message, got %q", out)
	}
}

func TestCmdEffortNoArg(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	cmdEffort(cmdHost(tui), nil)
	if out := testScrollOutput(tui); !strings.Contains(out, "current") {
		t.Errorf("expected current effort message, got %q", out)
	}
}

// --- /classifier-model ---

func TestCmdClassifierModelPinsAndClears(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	if a.ClassifierModel() != a.Model() {
		t.Fatalf("classifier model should default to the session model, got %q vs %q", a.ClassifierModel(), a.Model())
	}

	cmdClassifierModel(cmdHost(tui), []string{"tiny-classifier"})
	if got := a.ClassifierModel(); got != "tiny-classifier" {
		t.Errorf("classifier model = %q, want tiny-classifier", got)
	}
	if !a.ClassifierModelPinned() {
		t.Error("classifier model should report as pinned")
	}
	if a.Model() == "tiny-classifier" {
		t.Error("session model must not change")
	}
	if out := testScrollOutput(tui); !strings.Contains(out, "tiny-classifier") {
		t.Errorf("expected confirmation naming the model, got %q", out)
	}

	cmdClassifierModel(cmdHost(tui), []string{"default"})
	if a.ClassifierModelPinned() {
		t.Error("default should clear the pin")
	}
	if got := a.ClassifierModel(); got != a.Model() {
		t.Errorf("after reset classifier model = %q, want session model %q", got, a.Model())
	}
}

func TestCmdClassifierModelRejectsOtherProvider(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	cmdClassifierModel(cmdHost(tui), []string{"some-other-provider/m"})
	if a.ClassifierModelPinned() {
		t.Error("a foreign provider argument must not pin anything")
	}
	if out := testScrollOutput(tui); !strings.Contains(out, "current provider") {
		t.Errorf("expected a rejection explaining the provider, got %q", out)
	}
}

func TestCmdClassifierModelAcceptsOwnProviderPrefix(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	cmdClassifierModel(cmdHost(tui), []string{a.Provider().ID() + "/pinned-model"})
	if got := a.ClassifierModel(); got != "pinned-model" {
		t.Errorf("classifier model = %q, want pinned-model", got)
	}
}

func TestCmdClassifierModelNoArgReportsCurrent(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	cmdClassifierModel(cmdHost(tui), nil)
	out := testScrollOutput(tui)
	if !strings.Contains(out, a.ClassifierModel()) || !strings.Contains(out, "inherited") {
		t.Errorf("expected the inherited classifier model to be reported, got %q", out)
	}
}

// TestSlashClassifierModelRouting verifies /classifier-model with no argument
// opens the picker overlay, and with an argument pins the model directly —
// both while a turn could be running (no busy guard, unlike /model).
func TestSlashClassifierModelRouting(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	if err := tui.handleSlash("/classifier-model pinned-by-slash"); err != nil {
		t.Fatalf("handleSlash with arg: %v", err)
	}
	if got := a.ClassifierModel(); got != "pinned-by-slash" {
		t.Errorf("classifier model = %q, want pinned-by-slash", got)
	}

	if err := tui.handleSlash("/classifier-model"); err != nil {
		t.Fatalf("handleSlash no arg: %v", err)
	}
	if _, ok := tui.activeOverlay.(*pickerOverlay); !ok {
		t.Errorf("expected a picker overlay, got %T", tui.activeOverlay)
	}
}

// TestPaletteOpensClassifierModelPicker drives the whole Ctrl+P path: the
// palette lists /classifier-model, selecting it opens the classifier picker,
// and picking a row pins that model without touching the session model.
func TestPaletteOpensClassifierModelPicker(t *testing.T) {
	_, a, sid := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sid)

	tui.openCommandPalette()
	po, ok := tui.activeOverlay.(*paletteOverlay)
	if !ok {
		t.Fatalf("Ctrl+P did not open the palette, got %T", tui.activeOverlay)
	}
	po.filter = "classifier"
	po.idx = 0
	items := po.filtered()
	if len(items) != 1 || items[0].id != "/classifier-model" {
		t.Fatalf("palette filter matched %+v, want just /classifier-model", items)
	}
	if handled, _, cancel := po.feedKey(Key{Kind: KeyEnter}); !handled || cancel {
		t.Fatalf("palette Enter: handled=%v cancel=%v", handled, cancel)
	}
	picker, ok := tui.activeOverlay.(*pickerOverlay)
	if !ok {
		t.Fatalf("palette pick did not open the classifier picker, got %T", tui.activeOverlay)
	}
	if title := picker.titleForRender(); !strings.Contains(title, "classifier") {
		t.Errorf("picker title = %q, want it to mention the classifier", title)
	}

	// First row is the synthetic "default" (inherit) entry; a later row is a
	// real model. Picking a real model must pin exactly that model.
	rows := picker.filtered()
	if len(rows) < 2 || rows[0].id != "default" {
		t.Fatalf("picker rows = %+v, want a leading default row plus models", rows)
	}
	picker.idx = 1
	sessionModelBefore := a.Model()
	if handled, _, cancel := picker.feedKey(Key{Kind: KeyEnter}); !handled || cancel {
		t.Fatalf("picker Enter: handled=%v cancel=%v", handled, cancel)
	}
	if got := a.ClassifierModel(); got != rows[1].id {
		t.Errorf("classifier model = %q, want the picked row %q", got, rows[1].id)
	}
	if !a.ClassifierModelPinned() {
		t.Error("picking a model row should pin it")
	}
	if a.Model() != sessionModelBefore {
		t.Errorf("session model changed to %q, must stay %q", a.Model(), sessionModelBefore)
	}
}

// TestRefreshProviderUsageLimitsMarksHeaderDirty confirms
// refreshProviderUsageLimits (called by the lifecycle ticker on its regular
// 5-minute schedule; triggerUsageRefreshLocked calls the sibling
// refreshProviderUsageLimitsForce instead — see
// TestTriggerUsageRefreshLocked_ResetsTicker) runs to completion and marks
// the header dirty, even for a provider (the default FakeProvider) with no
// usage-limit data at all — RefreshAnthropicUsageLimits/RefreshOpenAIUsage
// Limits must be harmless no-ops rather than panicking or hanging.
func TestRefreshProviderUsageLimitsMarksHeaderDirty(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)
	tui.dirty = newDirtyTracker()

	tui.refreshProviderUsageLimits(context.Background())

	tui.mu.Lock()
	snap := tui.dirty.consume()
	tui.mu.Unlock()
	if !snap.status {
		t.Fatal("refreshProviderUsageLimits should have marked the header dirty")
	}
}

// TestTriggerUsageRefreshLocked_ResetsTicker confirms triggerUsageRefreshLocked
// sends a non-blocking reset signal on usageTickerReset, in addition to
// spawning the eager background refresh — this is what lets lifecycle.go's
// periodic ticker (when Run() is active) restart its 5-minute schedule from
// the moment of an explicit refresh instead of firing a redundant
// near-duplicate one shortly after on its own unrelated timeline. Calling it
// twice in a row must never block, even with nobody draining the channel.
func TestTriggerUsageRefreshLocked_ResetsTicker(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)

	tui.mu.Lock()
	tui.triggerUsageRefreshLocked()
	tui.triggerUsageRefreshLocked() // must not block despite the buffer being size 1
	tui.mu.Unlock()

	select {
	case <-tui.usageTickerReset:
	default:
		t.Fatal("triggerUsageRefreshLocked did not signal usageTickerReset")
	}
}

// TestProviderSwitchTriggersEagerUsageRefresh is the reported bug: switching
// providers via /model or /providers (both go through cmdModel ->
// refreshHostHeader) left the usage segment blank until the next scheduled
// 5-minute ticker fire, because a freshly constructed provider's usage
// cache always starts empty and nothing eagerly refetched it. Confirms
// refreshHostHeader now kicks off that refetch immediately (async, so this
// polls with a bound instead of asserting synchronously).
func TestProviderSwitchTriggersEagerUsageRefresh(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	configureAuth(t, "xai")
	tui := newTUIWithAgent(a, sessionID)
	tui.dirty = newDirtyTracker()

	cmdModel(cmdHost(tui), []string{"xai/grok-build"})

	// refreshHostHeader's own markFull() call already marks status dirty
	// synchronously, before triggerUsageRefreshLocked's goroutine ever runs —
	// consume (and discard) that first so a false positive from markFull
	// can't masquerade as the eager refresh actually having happened. The
	// signal under test is a *second*, later markStatus() call, which only
	// refreshProviderUsageLimitsForce's own goroutine can produce.
	tui.mu.Lock()
	tui.dirty.consume()
	tui.mu.Unlock()

	deadline := time.Now().Add(2 * time.Second)
	for {
		tui.mu.Lock()
		snap := tui.dirty.consume()
		tui.mu.Unlock()
		if snap.status {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("switching provider never triggered an eager usage-limit refresh")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
