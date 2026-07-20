package tui

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/agent"
	"github.com/mq37/poisson/internal/config"
	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/store"
	"github.com/mq37/poisson/internal/testutil"
	"github.com/mq37/poisson/internal/tools"
)

func newTestTUIHelper() *TUI {
	tui := newTUI(nil, "s-test", nil)
	tui.rows = 24
	tui.cols = 80
	tui.scrollRows = 20
	tui.writer = io.Discard
	return tui
}

func TestApproveLifecycle(t *testing.T) {
	tui := newTestTUIHelper()
	result := make(chan bool, 1)
	go func() {
		allowed, _ := tui.Approve("rm -rf x", "danger", "", agent.BashRiskHigh)
		result <- allowed
	}()

	deadline := time.Now().Add(500 * time.Millisecond)
	for !tui.approving.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !tui.approving.Load() {
		t.Fatal("Approve never entered approving state")
	}

	tui.approvalAnswer <- approvalReply{Allowed: true}

	var got bool
	select {
	case got = <-result:
	case <-time.After(time.Second):
		t.Fatal("Approve timed out")
	}
	if !got {
		t.Fatal("expected allow")
	}
	tui.mu.Lock()
	blocks := tui.scroll.blockCount()
	tui.mu.Unlock()
	if blocks != 0 {
		t.Fatalf("approval should not append to scrollback, blocks=%d", blocks)
	}
}

func TestScrollHandledBeforeApproval(t *testing.T) {
	tui := newTestTUIHelper()
	tui.mu.Lock()
	for i := 0; i < 30; i++ {
		tui.scroll.appendRaw(styleSystem, "line")
	}
	tui.mu.Unlock()

	if quit, err := tui.feed([]byte("\x1b[5~")); quit || err != nil {
		t.Fatalf("page up feed quit=%v err=%v", quit, err)
	}
	if tui.scroll.scrollOffset == 0 {
		t.Fatal("expected scroll offset > 0")
	}
}

func TestEditorDeleteKeyCSI(t *testing.T) {
	e := newEditor()
	e.setText("ab")
	e.col = 1
	consumed, submitted := e.handleEscape([]byte{27, '[', '3', '~'})
	if submitted || consumed != 4 {
		t.Fatalf("delete CSI: consumed=%d submitted=%v", consumed, submitted)
	}
	if e.text() != "a" {
		t.Fatalf("after delete: %q", e.text())
	}
}

func TestApproveWhileAgentRunning(t *testing.T) {
	tui := newTestTUIHelper()
	tui.status.Thinking = true
	result := make(chan bool, 1)
	go func() {
		allowed, _ := tui.Approve("rm -rf x", "danger", "", agent.BashRiskHigh)
		result <- allowed
	}()
	deadline := time.Now().Add(500 * time.Millisecond)
	for !tui.approving.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !tui.approving.Load() {
		t.Fatal("approving not set while agent running")
	}
	select {
	case tui.approvalAnswer <- approvalReply{Allowed: true}:
	default:
		t.Fatal("approval answer channel full")
	}
	select {
	case got := <-result:
		if !got {
			t.Fatal("expected allow")
		}
	case <-time.After(time.Second):
		t.Fatal("Approve timed out while agent running")
	}
}

func TestFeedArrowRightMovesCursor(t *testing.T) {
	tui := newTestTUIHelper()
	tui.editor.setText("abc")
	tui.editor.col = 1
	quit, err := tui.feed([]byte{27, '[', 'C'})
	if err != nil || quit {
		t.Fatalf("feed: quit=%v err=%v", quit, err)
	}
	if tui.editor.col != 2 {
		t.Fatalf("col=%d want 2", tui.editor.col)
	}
}

func TestFeedPlainArrowNotScrollback(t *testing.T) {
	tui := newTestTUIHelper()
	tui.scroll.appendRaw(styleSystem, "history")
	tui.scroll.scrollToBottom()
	before := tui.scroll.scrollOffset
	_, _ = tui.feed([]byte{27, '[', 'A'})
	if tui.scroll.scrollOffset != before {
		t.Fatalf("plain up scrolled offset %d -> %d", before, tui.scroll.scrollOffset)
	}
}

type streamCallCounter struct {
	mu    sync.Mutex
	calls int
}

type cancelOnCtxProvider struct {
	counter *streamCallCounter
}

func (p *cancelOnCtxProvider) ID() string { return "fake" }
func (p *cancelOnCtxProvider) Models() ([]provider.Model, error) {
	return []provider.Model{{ID: "m", ContextWindow: 8192}}, nil
}
func (p *cancelOnCtxProvider) Stream(ctx context.Context, _ *provider.Request) (<-chan provider.StreamEvent, error) {
	p.counter.mu.Lock()
	p.counter.calls++
	p.counter.mu.Unlock()
	ch := make(chan provider.StreamEvent)
	go func() {
		defer close(ch)
		<-ctx.Done()
	}()
	return ch, nil
}

func TestApproveCancelsRiskAssessment(t *testing.T) {
	testutil.TempHome(t)
	dir := testutil.TempDir(t)
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	sessionID := "s-risk-cancel"
	cfg := config.DefaultConfig()
	if err := st.CreateSession(&store.Session{
		ID: sessionID, Cwd: ".", Provider: "fake", Model: "m",
		CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	counter := &streamCallCounter{}
	prov := &cancelOnCtxProvider{counter: counter}
	a := agent.NewAgent(st, prov, tools.NewRegistry(), cfg, sessionID, nil, nil)
	a.SetModel("m")

	tui := newTUIWithAgent(a, sessionID)
	tui.writer = io.Discard

	result := make(chan bool, 1)
	go func() {
		allowed, _ := tui.Approve("git push origin main", "danger", "/tmp", agent.BashRiskUnknown)
		result <- allowed
	}()

	deadline := time.Now().Add(500 * time.Millisecond)
	for !tui.approving.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !tui.approving.Load() {
		t.Fatal("Approve never entered approving state")
	}

	// Wait until risk assessment has started at least one LLM stream.
	deadline = time.Now().Add(500 * time.Millisecond)
	for {
		counter.mu.Lock()
		calls := counter.calls
		counter.mu.Unlock()
		if calls >= 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}

	tui.approvalAnswer <- approvalReply{Allowed: true}
	select {
	case <-result:
	case <-time.After(time.Second):
		t.Fatal("Approve timed out")
	}

	// Give the risk goroutine time to observe cancellation.
	time.Sleep(50 * time.Millisecond)

	counter.mu.Lock()
	calls := counter.calls
	counter.mu.Unlock()
	if calls != 1 {
		t.Fatalf("Stream calls = %d, want 1 (second run should not start after approve)", calls)
	}
}

func TestApproveCancelledByRunCancel(t *testing.T) {
	tui := newTestTUIHelper()
	ctx, cancel := context.WithCancel(context.Background())
	tui.cancelMu.Lock()
	tui.cancelCtx = ctx
	tui.cancelRun = cancel
	tui.cancelMu.Unlock()

	result := make(chan bool, 1)
	go func() {
		allowed, _ := tui.Approve("rm -rf x", "danger", "", agent.BashRiskHigh)
		result <- allowed
	}()

	deadline := time.Now().Add(500 * time.Millisecond)
	for !tui.approving.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !tui.approving.Load() {
		t.Fatal("Approve never entered approving state")
	}
	cancel()

	select {
	case got := <-result:
		if got {
			t.Fatal("expected deny when run cancelled during approval")
		}
	case <-time.After(time.Second):
		t.Fatal("Approve timed out after cancel")
	}
}

// TestFeedDenyReasonKeyPassthroughWhenNotDenying verifies feedDenyReasonKey
// only takes over once the user has committed to denying (beginDenyReason) —
// otherwise the caller must fall through to its normal approval-key handling.
func TestFeedDenyReasonKeyPassthroughWhenNotDenying(t *testing.T) {
	tui := newTestTUIHelper()
	tui.mu.Lock()
	tui.activeOverlay = newApprovalOverlay("rm -rf x", "danger", "")
	tui.mu.Unlock()

	if tui.feedDenyReasonKey(Key{Kind: KeyRune, Rune: 'x'}) {
		t.Fatal("expected passthrough (false) before beginDenyReason")
	}
}

// TestFeedDenyReasonKeyAltBackspaceDeletesWord verifies the reason prompt is
// backed by the same editor as the main input box: Alt+Backspace deletes a
// whole word, not just the last rune (the bug this fixes — the old
// implementation only understood plain-rune append and single-char trim).
func TestFeedDenyReasonKeyAltBackspaceDeletesWord(t *testing.T) {
	tui := newTestTUIHelper()
	tui.mu.Lock()
	ao := newApprovalOverlay("rm -rf x", "danger", "")
	ao.beginDenyReason()
	tui.activeOverlay = ao
	tui.mu.Unlock()

	for _, r := range "too risky today" {
		if !tui.feedDenyReasonKey(Key{Kind: KeyRune, Rune: r}) {
			t.Fatalf("expected feedDenyReasonKey to handle rune %q", r)
		}
	}
	if !tui.feedDenyReasonKey(Key{Kind: KeyBackspace, Meta: true}) {
		t.Fatal("expected feedDenyReasonKey to handle Alt+Backspace")
	}

	tui.mu.Lock()
	if got := ao.reasonText(); got != "too risky " {
		t.Fatalf("reason = %q, want %q", got, "too risky ")
	}
	tui.mu.Unlock()
}

// TestRenderDenyReasonPanelStripsEmbeddedNewlines verifies a reason
// containing a literal newline (reasonEditor is the full multi-line editor —
// Shift+Enter or a multi-line paste can put one there) still renders as a
// single well-formed terminal row instead of leaking a raw \n into it.
func TestRenderDenyReasonPanelStripsEmbeddedNewlines(t *testing.T) {
	ao := newApprovalOverlay("rm -rf x", "danger", "")
	ao.beginDenyReason()
	ao.reasonEditor.insertText("line one\nline two")

	lines := ao.renderDenyReasonPanel(8, 60)
	for _, l := range lines {
		if strings.Contains(l, "\n") {
			t.Fatalf("rendered panel line contains a literal newline: %q", l)
		}
	}
	joined := strings.Join(lines, "")
	if !strings.Contains(joined, "line one line two") {
		t.Errorf("expected the newline replaced with a space, got: %q", joined)
	}
}

// TestFeedDenyReasonKeyTypesAndFinalizes drives the reason prompt directly:
// commit to deny, type a reason (with a backspace correction), then confirm
// with Enter — asserting the exact reply sent on approvalAnswer.
func TestFeedDenyReasonKeyTypesAndFinalizes(t *testing.T) {
	tui := newTestTUIHelper()
	tui.mu.Lock()
	ao := newApprovalOverlay("rm -rf x", "danger", "")
	ao.beginDenyReason()
	tui.activeOverlay = ao
	tui.mu.Unlock()

	for _, r := range "not nowz" {
		if !tui.feedDenyReasonKey(Key{Kind: KeyRune, Rune: r}) {
			t.Fatalf("expected feedDenyReasonKey to handle rune %q", r)
		}
	}
	if !tui.feedDenyReasonKey(Key{Kind: KeyBackspace}) {
		t.Fatal("expected feedDenyReasonKey to handle backspace")
	}

	tui.mu.Lock()
	if got := ao.reasonText(); got != "not now" {
		t.Fatalf("reason = %q, want %q", got, "not now")
	}
	tui.mu.Unlock()

	if !tui.feedDenyReasonKey(Key{Kind: KeyEnter}) {
		t.Fatal("expected feedDenyReasonKey to handle Enter")
	}

	select {
	case reply := <-tui.approvalAnswer:
		if reply.Allowed {
			t.Fatal("expected denial")
		}
		if reply.Reason != "not now" {
			t.Fatalf("reply.Reason = %q, want %q", reply.Reason, "not now")
		}
	default:
		t.Fatal("expected a reply on approvalAnswer")
	}
}

// TestApproveEndToEndDenyReason drives the whole path through Approve():
// commit to deny, type a reason, confirm with Enter, and check the reason
// comes back from Approve() itself — this is what bash.go forwards to the
// model as "command rejected by user - reason: ...".
func TestApproveEndToEndDenyReason(t *testing.T) {
	tui := newTestTUIHelper()
	type outcome struct {
		allowed bool
		reason  string
	}
	result := make(chan outcome, 1)
	go func() {
		allowed, reason := tui.Approve("rm -rf x", "danger", "", agent.BashRiskHigh)
		result <- outcome{allowed, reason}
	}()

	deadline := time.Now().Add(500 * time.Millisecond)
	for !tui.approving.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !tui.approving.Load() {
		t.Fatal("Approve never entered approving state")
	}

	// Commit to deny (mirrors the 'd' key handler in lifecycle.go), then type
	// a reason and confirm.
	tui.mu.Lock()
	ao, ok := tui.activeOverlay.(*approvalOverlay)
	if !ok {
		tui.mu.Unlock()
		t.Fatal("expected an approvalOverlay active")
	}
	ao.beginDenyReason()
	tui.mu.Unlock()

	for _, r := range "too risky" {
		tui.feedDenyReasonKey(Key{Kind: KeyRune, Rune: r})
	}
	tui.feedDenyReasonKey(Key{Kind: KeyEnter})

	select {
	case got := <-result:
		if got.allowed {
			t.Fatal("expected deny")
		}
		if got.reason != "too risky" {
			t.Fatalf("reason = %q, want %q", got.reason, "too risky")
		}
	case <-time.After(time.Second):
		t.Fatal("Approve timed out")
	}
}

// TestApproveEndToEndDenyEmptyReason verifies leaving the reason blank and
// pressing Enter denies with an empty reason — the "optional" part.
func TestApproveEndToEndDenyEmptyReason(t *testing.T) {
	tui := newTestTUIHelper()
	type outcome struct {
		allowed bool
		reason  string
	}
	result := make(chan outcome, 1)
	go func() {
		allowed, reason := tui.Approve("rm -rf x", "danger", "", agent.BashRiskHigh)
		result <- outcome{allowed, reason}
	}()

	deadline := time.Now().Add(500 * time.Millisecond)
	for !tui.approving.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !tui.approving.Load() {
		t.Fatal("Approve never entered approving state")
	}

	tui.mu.Lock()
	ao, _ := tui.activeOverlay.(*approvalOverlay)
	ao.beginDenyReason()
	tui.mu.Unlock()

	tui.feedDenyReasonKey(Key{Kind: KeyEnter}) // no reason typed

	select {
	case got := <-result:
		if got.allowed {
			t.Fatal("expected deny")
		}
		if got.reason != "" {
			t.Fatalf("reason = %q, want empty", got.reason)
		}
	case <-time.After(time.Second):
		t.Fatal("Approve timed out")
	}
}

// TestDenyWithReasonLeavesRunRunning verifies that denying with a non-empty
// reason lets the model see the rejection and keep going, instead of cutting
// the turn off — only an EMPTY reason (Ctrl+C's panic-deny, or confirming an
// unfilled reason prompt) should cancel the in-flight run.
func TestDenyWithReasonLeavesRunRunning(t *testing.T) {
	tui := newTestTUIHelper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	tui.cancelMu.Lock()
	tui.cancelCtx = ctx
	tui.cancelRun = cancel
	tui.cancelMu.Unlock()

	type outcome struct {
		allowed bool
		reason  string
	}
	result := make(chan outcome, 1)
	go func() {
		allowed, reason := tui.Approve("rm -rf x", "danger", "", agent.BashRiskHigh)
		result <- outcome{allowed, reason}
	}()

	deadline := time.Now().Add(500 * time.Millisecond)
	for !tui.approving.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !tui.approving.Load() {
		t.Fatal("Approve never entered approving state")
	}

	tui.mu.Lock()
	ao, _ := tui.activeOverlay.(*approvalOverlay)
	ao.beginDenyReason()
	tui.mu.Unlock()

	for _, r := range "not now" {
		tui.feedDenyReasonKey(Key{Kind: KeyRune, Rune: r})
	}
	tui.feedDenyReasonKey(Key{Kind: KeyEnter})

	select {
	case got := <-result:
		if got.allowed {
			t.Fatal("expected deny")
		}
		if got.reason != "not now" {
			t.Fatalf("reason = %q, want %q", got.reason, "not now")
		}
	case <-time.After(time.Second):
		t.Fatal("Approve timed out")
	}

	if err := ctx.Err(); err != nil {
		t.Fatalf("run context was cancelled (%v) despite a non-empty deny reason", err)
	}
}

// TestDenyWithEmptyReasonCancelsRun verifies the pre-existing "deny stops
// the turn" behavior is preserved when no reason is given.
func TestDenyWithEmptyReasonCancelsRun(t *testing.T) {
	tui := newTestTUIHelper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	tui.cancelMu.Lock()
	tui.cancelCtx = ctx
	tui.cancelRun = cancel
	tui.cancelMu.Unlock()

	result := make(chan bool, 1)
	go func() {
		allowed, _ := tui.Approve("rm -rf x", "danger", "", agent.BashRiskHigh)
		result <- allowed
	}()

	deadline := time.Now().Add(500 * time.Millisecond)
	for !tui.approving.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !tui.approving.Load() {
		t.Fatal("Approve never entered approving state")
	}

	tui.mu.Lock()
	ao, _ := tui.activeOverlay.(*approvalOverlay)
	ao.beginDenyReason()
	tui.mu.Unlock()

	tui.feedDenyReasonKey(Key{Kind: KeyEnter}) // no reason typed

	select {
	case got := <-result:
		if got {
			t.Fatal("expected deny")
		}
	case <-time.After(time.Second):
		t.Fatal("Approve timed out")
	}

	if err := ctx.Err(); err == nil {
		t.Fatal("expected the run context to be cancelled after an empty-reason deny")
	}
}

func TestSplitPrefixUnicode(t *testing.T) {
	line := "prefix @café"
	col := len([]rune(line))
	_, tok := splitPrefix(line, col)
	if tok != "@café" {
		t.Fatalf("token = %q, want %q", tok, "@café")
	}
}
