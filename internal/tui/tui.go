package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/term"
	"poisson/internal/agent"
)

// contentWidth is the safe scrollback line width. Writing exactly cols runes at
// column 1 makes many terminals auto-wrap, so scroll content stays cols-1.
func (t *TUI) contentWidth() int {
	w := t.cols - 1
	if w < 1 {
		return 1
	}
	return w
}

// TUI is the split-screen alt-screen REPL.
type TUI struct {
	agent     *agent.Agent
	sessionID string
	output    chan agent.OutputEvent
	writer    io.Writer
	fd        int
	oldState  *term.State

	// Layout state, recomputed on resize.
	mu         sync.Mutex
	rows       int // total terminal rows
	cols       int // total terminal cols
	headerRows int // Grok-style top strip (cwd · tokens · time)
	scrollRows int // rows allotted to scrollback
	inputRows  int // rows for multi-line input
	statusRows int // legacy; 0 (status moved to headerRows)

	// Region content.
	scroll *scrollback
	editor *editor
	status StatusSnapshot
	dirty  dirtyTracker
	keyDec Decoder

	renderFrame    int
	activeTools    int
	nextToolID     int64
	lastInputRows  int
	activeOverlay  overlay

	// Lifecycle.
	stopped atomic.Bool
	done    chan struct{} // closed by input goroutine when it exits

	// History.
	history    []string
	histIdx    int
	draftSaved string // restore on arrow-down past newest

	// Completion dropdown (slash commands, @file paths).
	completion           *completion
	lastCompletionRows   int // scroll rows last painted by the dropdown

	// Focus: Tab toggles between input editor and conversation scroll.
	focusRegion focusRegion
	convUserIdx int // index into scroll.userBlockIndices()

	hintExpiry time.Time // when set, ephemeral status.Hint auto-clears

	// Approval coordination. The input goroutine is the sole stdin reader;
	// when approval is pending it routes the answer through this channel
	// instead of feeding it to the editor.
	approvalMu     sync.Mutex
	approving      atomic.Bool
	approvalAnswer chan bool
	overlayQuit    atomic.Bool

	// Submission signaling.
	lastCtrlC time.Time

	// cancelRun/cancelCtx are set while an agent prompt is in flight. The input
	// goroutine uses them to cancel a running request (and pending approval) on Ctrl+C.
	cancelCtx  context.Context
	cancelRun  context.CancelFunc
	cancelMu   sync.Mutex
}

// NewTUI constructs the interactive TUI wired to the given agent and output channel.
func NewTUI(a *agent.Agent, sessionID string, outputChan chan agent.OutputEvent) *TUI {
	return newTUI(a, sessionID, outputChan)
}

func newTUI(a *agent.Agent, sessionID string, outputChan chan agent.OutputEvent) *TUI {
	theme := "dark"
	showTokens := true
	showCost := true
	if a != nil {
		if c := a.Config(); c != nil {
			if c.TUI.Theme != "" {
				theme = c.TUI.Theme
			}
			showTokens = c.TUI.ShowTokens
			showCost = c.TUI.ShowCost
		}
	}
	applyTheme(theme)

	return &TUI{
		agent:          a,
		sessionID:      sessionID,
		output:         outputChan,
		writer:         os.Stdout,
		fd:             int(os.Stdin.Fd()),
		scroll:         newScrollback(8192),
		editor:         newEditor(),
		history:        []string{},
		histIdx:        -1,
		status: StatusSnapshot{
			SessionID:  sessionID,
			Cwd:        cwdOf(),
			Model:      modelLabel(a),
			ShowTokens: showTokens,
			ShowCost:   showCost,
		},
		dirty:          newDirtyTracker(),
		inputRows:      3,
		headerRows:     1,
		statusRows:     0,
		lastInputRows:  3,
		done:           make(chan struct{}),
		approvalAnswer: make(chan bool, 1),
	}
}

// modelLabel returns a short "provider/model" label for status display.
func modelLabel(a *agent.Agent) string {
	if a == nil {
		return "-"
	}
	return a.Provider().ID() + "/" + a.Model()
}

// inputHeight returns how many screen rows the input currently needs.
// Caps at a third of total rows so the scrollback stays readable.
func (t *TUI) inputHeight(width int) int {
	n := totalVisualLines(t.editor, width) + 2 // +1 separator, +1 hint
	if n < 4 {
		n = 4
	}
	max := t.rows / 3
	if max < 5 {
		max = 5
	}
	if n > max {
		n = max
	}
	return n
}

// Run starts the alt-screen TUI. It blocks until the user exits.
func (t *TUI) Run() error {
	oldState, err := term.MakeRaw(t.fd)
	if err != nil {
		return fmt.Errorf("enter raw mode: %w", err)
	}
	t.oldState = oldState
	t.recomputeLayout()

	// Wire terminal mode.
	t.writeRaw(altScreenOn + hideCursor + bracketedOn + kittyKbOn + mouseOn)
	t.installResize()

	// Restore terminal on any exit path — including panic — so the user's
	// shell isn't left in raw alt-screen with kitty keyboard enabled.
	defer func() {
		t.stopped.Store(true)
		t.writeRaw(mouseOff + kittyKbOff + bracketedOff + showCursor + altScreenOff)
		_ = term.Restore(t.fd, t.oldState)
	}()

	// Lifecycle channel. render/input goroutines exit when this is closed.
	stop := make(chan struct{})

	// Initial paint before starting goroutines so wrapWidth is set.
	t.dirty.markFull()
	t.paint(t.dirty.consume())

	// Render goroutine: 30fps redraw on dirty; animates spinners while busy.
	renderDone := make(chan struct{})
	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "poisson: render panic: %v\n", r)
			}
			close(renderDone)
		}()
		tick := time.NewTicker(33 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				t.mu.Lock()
				animate := needsSpinner(t.status.Thinking, t.activeTools)
				t.mu.Unlock()
				if animate {
					t.renderFrame++
					t.markSpinnerTick()
				}
				snap := t.dirty.consume()
				if snap.any() {
					t.paint(snap)
				}
			}
		}
	}()

	// Input loop.
	go func() {
		defer close(t.done)
		buf := make([]byte, 65536)
		for {
			select {
			case <-stop:
				return
			default:
			}
			n, err := os.Stdin.Read(buf)
			if err != nil {
				return
			}
			if handled := t.handleMouseInput(buf[:n]); handled {
				continue
			}
			// Don't scroll scrollback behind modal overlays or approval.
			t.mu.Lock()
			blockBG := t.blocksBackgroundInput()
			t.mu.Unlock()
			if !blockBG {
				viewport := t.scrollViewportRows()
				if delta, ok := parseScrollInputRaw(buf[:n], viewport); ok {
					t.handleScrollDelta(delta)
					continue
				}
			}
			// If an approval prompt is active, route recognized answers to the
			// approval channel instead of feeding them to the editor.
			for _, k := range t.keyDec.Push(buf[:n]) {
				if t.approving.Load() {
					if k.isCtrlC() {
						t.cancelActiveRun()
						continue
					}
					allowed, ok := keyApprovalAnswer(k)
					if ok {
						t.approvalAnswer <- allowed
					} else {
						t.flashApprovalHint()
					}
					continue
				}
				quit, err := t.feedKey(k)
				if err != nil {
					t.appendError(err)
					continue
				}
				if quit {
					return
				}
			}
		}
	}()

	// Run loop: sole reader of t.output. Drains agent events until input exits.
	for {
		select {
		case <-t.done:
			close(stop)
			<-renderDone
			return nil
		case ev, ok := <-t.output:
			if !ok {
				t.output = nil
				continue
			}
			t.mu.Lock()
			t.handleEvent(ev)
			t.markAfterEvent(ev)
			t.mu.Unlock()
		}
	}
}

// feed decodes bytes via the shared key decoder and dispatches each event.
// Tests may call this directly; the input loop uses feedKey per decoded key.
func (t *TUI) feed(data []byte) (bool, error) {
	for _, k := range t.keyDec.Push(data) {
		if quit, err := t.feedKey(k); quit || err != nil {
			return quit, err
		}
	}
	return false, nil
}

// feedKey handles one normalized key event. It returns (quit, error).
func (t *TUI) feedKey(k Key) (bool, error) {
	t.maybeClearHint()
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.editor.wrapWidth < 1 && t.cols > 0 {
		w := t.cols - 1
		if w < 1 {
			w = 1
		}
		t.editor.wrapWidth = w
	}

	if t.blocksBackgroundInput() || t.hasKeyOverlay() {
		if k.Kind == KeyPaste {
			if t.handleOverlayPaste(k) {
				if _, ok := t.activeOverlay.(*searchOverlay); ok {
					t.markScrollDirty()
				} else {
					t.dirty.markFull()
				}
			}
			return false, nil
		}
	}

	if t.handleKeyOverlay(k) {
		if t.overlayQuit.Load() {
			t.overlayQuit.Store(false)
			return true, nil
		}
		return false, nil
	}

	if t.hasKeyOverlay() {
		if k.isCtrlC() || k.Kind == KeyEscape {
			t.dismissOverlay()
		}
		return false, nil
	}

	if t.focusRegion == focusConv && t.feedConvFocus(k) {
		return false, nil
	}

	w := t.contentWidth()
	if t.scroll.focusedToolExpanded(w) {
		switch k.Kind {
		case KeyArrowUp:
			if t.scroll.scrollFocusedTool(w, -1) {
				t.markScrollDirty()
			}
			return false, nil
		case KeyArrowDown:
			if t.scroll.scrollFocusedTool(w, 1) {
				t.markScrollDirty()
			}
			return false, nil
		}
	}
	if k.Kind == KeyEscape && t.scroll.collapseFocusedTool() {
		t.markScrollDirty()
		return false, nil
	}

	if !t.completion.empty() {
		switch k.Kind {
		case KeyArrowUp:
			t.completion.cycle(-1)
			t.markInputDirty()
			return false, nil
		case KeyArrowDown:
			t.completion.cycle(+1)
			t.markInputDirty()
			return false, nil
		}
	}

	if delta, ok := scrollDeltaForKey(k, t.scrollRows); ok {
		t.scrollByDelta(delta)
		return false, nil
	}

	if k.Kind == KeyTab {
		t.handleTabKey()
		return false, nil
	}

	if !t.completion.empty() && t.completion.idx >= 0 && k.isEnter() {
		t.applyCompletion(t.completion.cands[t.completion.idx])
		t.completion = nil
		t.markInputDirty()
		return false, nil
	}

	if k.Kind == KeyEscape && t.completion != nil && !t.completion.empty() {
		t.completion = nil
		t.markInputDirty()
		return false, nil
	}

	if k.isCtrlC() {
		if t.editor.text() != "" {
			t.editor.setText("")
			t.completion = nil
			t.markInputDirty()
		} else {
			now := time.Now()
			if !t.lastCtrlC.IsZero() && now.Sub(t.lastCtrlC) <= 2*time.Second {
				return true, nil
			}
			t.lastCtrlC = now
			t.setEphemeralHintLocked("Ctrl+C again to exit", 4*time.Second)
		}
		return false, nil
	}

	if k.Kind == KeyCtrl && k.Byte == 20 {
		if t.scroll.toggleThinkingInView(t.scrollRows, t.contentWidth()) {
			t.markScrollDirty()
		}
		return false, nil
	}

	if t.focusRegion == focusConv && k.Kind == KeyCtrl && k.Byte == 5 {
		if t.scroll.toggleToolExpandInView(t.convScrollRows(), t.contentWidth()) {
			t.markScrollDirty()
		}
		return false, nil
	}

	if t.completion.empty() && k.Kind == KeyCtrl && k.Byte == 6 {
		t.openSearch()
		return false, nil
	}

	if k.Kind == KeyCtrl && k.Byte == 25 {
		t.yankClipboard()
		return false, nil
	}

	if t.completion.empty() && k.Kind == KeyCtrl && (k.Byte == 16 || k.Byte == 30) {
		t.openCommandPalette()
		return false, nil
	}

	if t.completion.empty() && t.activeOverlay == nil && k.Kind == KeyCtrl {
		switch k.Byte {
		case 13:
			t.openModelPicker()
			return false, nil
		case 19:
			t.openSessionPicker()
			return false, nil
		case 18:
			t.navigateHistory(-1)
			t.markInputDirty()
			return false, nil
		case 14:
			t.navigateHistory(1)
			t.markInputDirty()
			return false, nil
		}
	}

	if t.completion.empty() && t.activeOverlay == nil && t.focusRegion == focusInput {
		switch k.Kind {
		case KeyArrowUp:
			if t.editorAtScrollTop() {
				t.navigateHistory(-1)
				t.markInputDirty()
			}
			return false, nil
		case KeyArrowDown:
			if t.editorAtScrollBottom() {
				t.navigateHistory(1)
				t.markInputDirty()
			}
			return false, nil
		}
	}

	if t.running() && !t.approving.Load() {
		if k.isCtrlC() {
			t.cancelActiveRunLocked()
			t.lastCtrlC = time.Now()
			return false, nil
		}
		if k.isEnter() {
			return false, nil
		}
		return t.processEditorKey(k)
	}

	return t.processEditorKey(k)
}

// processEditorKey applies one key to the editor and handles submit/quit.
func (t *TUI) processEditorKey(k Key) (bool, error) {
	submitted, quit := t.editor.applyKey(k)
	if submitted != "" {
		t.completion = nil
		if err := t.submit(submitted); err != nil {
			if errors.Is(err, errQuitSentinel) {
				return true, nil
			}
			t.appendErrorLocked(err)
			return false, nil
		}
		t.refreshCompletion()
		t.markInputDirty()
		return false, nil
	}
	if quit {
		return true, nil
	}
	t.refreshCompletion()
	t.markInputDirty()
	return false, nil
}

// processEditor feeds legacy raw bytes to the editor (tests only).
func (t *TUI) processEditor(data []byte) (bool, error) {
	for _, k := range (&Decoder{}).Push(data) {
		if quit, err := t.processEditorKey(k); quit || err != nil {
			return quit, err
		}
	}
	return false, nil
}

// hasCSI reports whether data contains any CSI sequence (ESC[).
func hasCSI(data []byte) bool {
	for i := 0; i+1 < len(data); i++ {
		if data[i] == 27 && data[i+1] == '[' {
			return true
		}
	}
	return false
}

// containsCtrlC reports whether data contains a Ctrl+C byte outside a
// bracketed-paste region. A pasted 0x03 should not exit the editor.
func containsCtrlC(data []byte) bool {
	inPaste := false
	for i := 0; i < len(data); i++ {
		b := data[i]
		if !inPaste && i+5 < len(data) && b == 27 && data[i+1] == '[' && data[i+2] == '2' && data[i+3] == '0' && data[i+4] == '0' && data[i+5] == '~' {
			inPaste = true
			i += 5
			continue
		}
		if inPaste && i+5 < len(data) && b == 27 && data[i+1] == '[' && data[i+2] == '2' && data[i+3] == '0' && data[i+4] == '1' && data[i+5] == '~' {
			inPaste = false
			i += 5
			continue
		}
		if !inPaste && b == 3 {
			return true
		}
	}
	return false
}

// containsSubmitKey reports whether data contains a plain Enter/Return key.
// Handles both \r (raw) and kitty keyboard Enter (ESC[13u or ESC[13;1u).
// Shift+Enter (ESC[13;2u) does NOT match.
func containsSubmitKey(data []byte) bool {
	for i, b := range data {
		if b == '\r' {
			return true
		}
		// kitty: ESC [ 1 3 u  (plain Enter)
		if b == 27 && i+4 < len(data) && data[i+1] == '[' && data[i+2] == '1' && data[i+3] == '3' && data[i+4] == 'u' {
			return true
		}
		// kitty: ESC [ 1 3 ; 1 u  (plain Enter, explicit mods)
		if b == 27 && i+6 < len(data) && data[i+1] == '[' && data[i+2] == '1' && data[i+3] == '3' && data[i+4] == ';' && data[i+5] == '1' && data[i+6] == 'u' {
			return true
		}
	}
	return false
}

// handleTab triggers or accepts completion. Returns true if the caller should
// schedule a redraw.
func (t *TUI) handleTab() bool {
	if t.completion == nil || t.completion.empty() {
		t.refreshCompletion()
		if t.completion == nil || t.completion.empty() {
			return false
		}
		// If only one candidate, accept it immediately.
		if len(t.completion.cands) == 1 {
			t.acceptCompletion()
			return true
		}
		// Expand to the common prefix.
		if lcp := commonPrefixCands(t.completion.cands); len(lcp) > len(t.completion.prefix) {
			t.applyCompletion(lcp)
			t.refreshCompletion()
			if t.completion == nil || t.completion.empty() {
				return true
			}
		}
		t.completion.idx = 0
		return true
	}
	t.acceptCompletion()
	return true
}

// refreshCompletion rebuilds the candidate list from the current editor text.
func (t *TUI) refreshCompletion() {
	cwd, _ := os.Getwd()
	line := t.editor.lines[t.editor.row]
	prefix, token := splitPrefix(line, t.editor.col)
	var cands []string
	var kind completionKind
	truncated := false
	switch {
	case strings.HasPrefix(token, "/") && !strings.ContainsAny(token, " \t"):
		cands = matchSlash(token)
		if len(cands) > 0 {
			kind = completionSlash
		}
	case strings.ContainsRune(token, '@'):
		cands, truncated = matchAtFileFuzzy(token, cwd)
		if len(cands) > 0 {
			kind = completionAtFile
		}
	}
	if len(cands) == 0 {
		t.completion = nil
		return
	}
	idx := -1
	if t.completion != nil && t.completion.kind == kind && t.completion.prefix == prefix {
		idx = t.completion.idx
		if idx >= len(cands) {
			idx = -1
		}
	}
	t.completion = &completion{kind: kind, prefix: prefix, cands: cands, idx: idx, truncated: truncated}
}

// splitPrefix returns (text-before-cursor-on-this-row, partial token at cursor).
// col is in runes (matches editor.col).
func splitPrefix(line string, col int) (string, string) {
	runes := []rune(line)
	if col > len(runes) {
		col = len(runes)
	}
	if col < 0 {
		col = 0
	}
	head := string(runes[:col])
	i := col - 1
	for i >= 0 && runes[i] != ' ' && runes[i] != '\t' && runes[i] != '\n' {
		i--
	}
	return head, string(runes[i+1 : col])
}

// acceptCompletion inserts the selected candidate at the cursor, replacing
// the partial token.
func (t *TUI) acceptCompletion() {
	if t.completion == nil || t.completion.empty() || t.completion.idx < 0 {
		return
	}
	t.applyCompletion(t.completion.cands[t.completion.idx])
	t.refreshCompletion()
}

// applyCompletion replaces the partial token before the cursor with s.
func (t *TUI) applyCompletion(s string) {
	line := t.editor.lines[t.editor.row]
	if t.completion != nil && strings.HasPrefix(s, t.completion.prefix) {
		// Completion result extends what the user already typed; insert only the delta.
		delta := strings.TrimPrefix(s, t.completion.prefix)
		t.editor.insertText(delta)
		return
	}
	// Fallback: replace the whole partial token.
	runes := []rune(line)
	if t.editor.col > len(runes) {
		t.editor.col = len(runes)
	}
	tail := string(runes[t.editor.col:])
	tokenStart := 0
	for i := t.editor.col - 1; i >= 0; i-- {
		if runes[i] == ' ' || runes[i] == '\t' {
			tokenStart = i + 1
			break
		}
	}
	newLine := string(runes[:tokenStart]) + s + tail
	t.editor.lines[t.editor.row] = newLine
	t.editor.col = tokenStart + len([]rune(s))
}

func (t *TUI) submit(text string) error {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		t.editor.setText("")
		return nil
	}
	if strings.HasPrefix(trimmed, "/") {
		t.scroll.scrollToBottom()
		t.scroll.append(StyledLine{Style: styleUser, Text: text})
		t.editor.setText("")
		t.histIdx = -1
		t.draftSaved = ""
		return t.handleSlash(trimmed)
	}
	expanded, err := expandAtFiles(text)
	if err != nil {
		t.appendErrorLocked(err)
		t.editor.setText("")
		return nil
	}
	t.history = append(t.history, text)
	t.histIdx = -1
	t.draftSaved = ""
	t.scroll.scrollToBottom()
	t.scroll.append(StyledLine{Style: styleUser, Text: text})
	t.editor.setText("")

	// Start the agent turn. feed (our caller) holds t.mu, so we can set
	// status.Thinking directly. The agent runs in its own goroutine; the Run
	// loop drains output events.
	t.status.Thinking = true
	t.status.Hint = ""
	t.markScrollDirty()
	t.dirty.markStatus()

	ctx, cancel := context.WithCancel(context.Background())
	t.cancelMu.Lock()
	t.cancelCtx = ctx
	t.cancelRun = cancel
	t.cancelMu.Unlock()

	go func() {
		defer func() {
			t.cancelMu.Lock()
			t.cancelCtx = nil
			t.cancelRun = nil
			t.cancelMu.Unlock()
			cancel()
			t.mu.Lock()
			t.status.Thinking = false
			t.dirty.markStatus()
			t.mu.Unlock()
			if r := recover(); r != nil {
				t.mu.Lock()
				t.scroll.appendRaw(styleError, fmt.Sprintf("agent panic: %v", r))
				t.mu.Unlock()
			}
		}()
		// The agent sends OutputError on all error paths; the Run loop displays
		// them. We just wait for completion and clean up.
		_ = t.agent.PromptWithContext(ctx, expanded)
	}()
	return nil
}

// handleEvent appends agent output to the scrollback. Caller must hold t.mu.
func (t *TUI) handleEvent(ev agent.OutputEvent) {
	switch ev.Type {
	case agent.OutputText:
		t.scroll.finalizeThinking()
		t.scroll.append(StyledLine{Style: styleAssistant, Text: ev.Text})
	case agent.OutputThinking:
		t.scroll.append(StyledLine{Style: styleThinking, Text: ev.Text})
	case agent.OutputToolStart:
		t.scroll.finalizeThinking()
		id := t.nextToolID
		t.nextToolID++
		t.scroll.appendToolCall(id, ev.ToolCallID, ev.ToolName, ev.ToolInput)
	case agent.OutputToolResult:
		t.scroll.completeToolCall(ev.ToolCallID, ev.ToolResultContent, ev.ToolError, 0)
	case agent.OutputApproval:
		// Approval UI is shown via activeOverlay in Approve().
	case agent.OutputError:
		t.scroll.appendRaw(styleError, "error: "+ev.Text)
	case agent.OutputCompacting:
		t.scroll.appendRaw(styleCompacting, "  compacting context...")
	case agent.OutputStatus:
		// applied in markAfterEvent
	}
}

// Approve renders an approval prompt for a dangerous bash command and waits
// for the user's answer. The input goroutine is the sole stdin reader; it
// routes the answer through t.approvalAnswer when t.approving is set.
func (t *TUI) Approve(command, description string) bool {
	t.approvalMu.Lock()
	defer t.approvalMu.Unlock()

	select {
	case <-t.approvalAnswer:
	default:
	}

	// Signal before paint so the input goroutine routes keys here immediately
	// (running() would otherwise swallow them during tool execution).
	t.approving.Store(true)
	defer t.approving.Store(false)

	t.mu.Lock()
	t.activeOverlay = newApprovalOverlay(command, description)
	t.dirty.markFull()
	t.mu.Unlock()

	t.cancelMu.Lock()
	runCtx := t.cancelCtx
	t.cancelMu.Unlock()

	var cancelCh <-chan struct{}
	if runCtx != nil {
		cancelCh = runCtx.Done()
	}

	var allowed bool
	select {
	case allowed = <-t.approvalAnswer:
	case <-t.done:
		t.mu.Lock()
		t.activeOverlay = nil
		t.mu.Unlock()
		return false
	case <-cancelCh:
		allowed = false
	}

	t.mu.Lock()
	t.activeOverlay = nil
	t.scroll.appendRaw(styleSystem, formatApprovalResult(allowed))
	t.markScrollDirty()
	t.mu.Unlock()
	return allowed
}

// navigateHistory loads a previous/next prompt into the editor. dir=-1 is
// older; dir=+1 is newer.
func (t *TUI) navigateHistory(dir int) {
	if len(t.history) == 0 {
		return
	}
	if t.histIdx == -1 {
		t.histIdx = len(t.history)
		t.draftSaved = t.editor.text()
	}
	t.histIdx += dir
	if t.histIdx < 0 {
		t.histIdx = 0
	}
	if t.histIdx >= len(t.history) {
		t.histIdx = len(t.history)
		t.editor.setText(t.draftSaved)
		return
	}
	t.editor.setText(t.history[t.histIdx])
}

// running reports whether an agent prompt is in flight.
func (t *TUI) running() bool { return t.status.Thinking }

// --- Layout ---

func (t *TUI) recomputeLayout() {
	t.mu.Lock()
	defer t.mu.Unlock()
	w, h, err := term.GetSize(t.fd)
	if err != nil || w < 40 || h < 10 {
		w, h = 80, 24
	}
	t.rows = h
	t.cols = w
	wrapWidth := w - 1
	if wrapWidth < 1 {
		wrapWidth = 1
	}
	t.editor.wrapWidth = wrapWidth
	t.headerRows = 1
	t.statusRows = 0
	t.inputRows = t.inputHeight(wrapWidth)
	t.scrollRows = h - t.headerRows - t.inputRows
	if t.scrollRows < 3 {
		t.scrollRows = 3
	}
	t.scroll.clampScrollOffset(t.convScrollRows(), t.contentWidth())
}

func (t *TUI) installResize() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGWINCH)
	go func() {
		defer signal.Stop(sig)
		for {
			select {
			case _, ok := <-sig:
				if !ok {
					return
				}
				if t.stopped.Load() {
					return
				}
				t.recomputeLayout()
				t.markFullDirty()
				if t.done != nil {
					select {
					case <-t.done:
						return
					default:
					}
				}
			}
		}
	}()
}

func (t *TUI) renderCompletion(c *completion) string {
	var b strings.Builder
	count := fmt.Sprintf("%d", len(c.cands))
	if c.truncated {
		count += "+"
	}
	header := fmt.Sprintf(" %s (%s) ", prefixName(c.kind), count)
	b.WriteString(bgDarkRed)
	b.WriteString(fgBlack)
	b.WriteString(bold)
	b.WriteString(header)
	b.WriteString(reset)
	b.WriteString("\n")
	for i, cand := range c.cands {
		marker := "  "
		style := ""
		if i == c.idx {
			marker = "▶ "
			style = fgCyan + bold
		}
		b.WriteString(style)
		b.WriteString(marker)
		b.WriteString(cand)
		b.WriteString(reset)
		b.WriteString("\n")
	}
	return b.String()
}

func prefixName(k completionKind) string {
	switch k {
	case completionSlash:
		return "commands"
	case completionAtFile:
		return "files"
	}
	return "?"
}

func (t *TUI) renderInputHeader() string {
	return ""
}

func (t *TUI) renderInputScreenRow(lineIdx int, screenLines []string, sr, sc int) string {
	if lineIdx >= len(screenLines) {
		return ""
	}
	line := screenLines[lineIdx]
	runes := []rune(line)
	prompt := ""
	if lineIdx == 0 {
		prompt = fgGreen + "› " + reset
	}
	if lineIdx != sr {
		if lineIdx == 0 {
			return prompt + string(runes)
		}
		return " " + string(runes)
	}
	if sc < 0 {
		sc = 0
	}
	prefix := string(runes[:min(sc, len(runes))])
	suffix := ""
	if sc < len(runes) {
		suffix = string(runes[sc+1:])
	}
	var b strings.Builder
	b.WriteString(prompt)
	b.WriteString(prefix)
	b.WriteString("\x1b[7m")
	if sc < len(runes) {
		b.WriteRune(runes[sc])
	} else {
		b.WriteRune(' ')
	}
	b.WriteString("\x1b[27m")
	b.WriteString(suffix)
	return b.String()
}

func (t *TUI) renderHintLine() string {
	if t.focusRegion == focusConv {
		return dim + "Tab:input · PgUp/Dn:scroll · Shift+←/→:prompts · Ctrl+E:tool" + reset
	}
	base := "Tab:conv · Enter:send · ↑↓:history · Ctrl+Y:yank · Ctrl+F:find · Ctrl+P:palette · Ctrl+S:sessions · Ctrl+M:model"
	if t.status.Hint != "" {
		return dim + t.status.Hint + " · " + base + reset
	}
	return dim + base + reset
}

// scrollByDelta scrolls the scrollback viewport. Caller must hold t.mu.
func (t *TUI) scrollByDelta(delta int) {
	if delta > 0 {
		t.scroll.scrollUp(delta)
	} else if delta < 0 {
		t.scroll.scrollDown(-delta)
	}
	t.scroll.clampScrollOffset(t.convScrollRows(), t.contentWidth())
	t.markScrollDirty()
}

func (t *TUI) handleScrollDelta(delta int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.scrollByDelta(delta)
}

func (t *TUI) editorAtScrollTop() bool {
	if t.editor.row != 0 {
		return false
	}
	sr, _ := screenCursor(t.editor, t.editor.wrapWidth)
	return sr == 0
}

func (t *TUI) editorAtScrollBottom() bool {
	if t.editor.row != len(t.editor.lines)-1 {
		return false
	}
	sr, _ := screenCursor(t.editor, t.editor.wrapWidth)
	last := totalVisualLines(t.editor, t.editor.wrapWidth) - 1
	return sr >= last
}

// appendErrorLocked writes an error to the scrollback. The caller MUST hold
// t.mu (e.g. feed/submit/processEditor, which run under the lock).
func (t *TUI) appendErrorLocked(err error) {
	t.scroll.appendRaw(styleError, "error: "+err.Error())
	t.markScrollDirty()
}

// appendError writes an error to the scrollback, taking t.mu. Call only when
// NOT already holding the lock (e.g. the input goroutine after feed returns).
func (t *TUI) appendError(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.appendErrorLocked(err)
}

func (t *TUI) writeRaw(s string) {
	if t.writer != nil {
		_, _ = t.writer.Write([]byte(s))
	}
}

func (t *TUI) yankClipboard() {
	t.mu.Lock()
	text := t.scroll.yankText()
	t.mu.Unlock()
	if text == "" {
		t.setEphemeralHint("nothing to yank", 2*time.Second)
		return
	}
	_ = osc52Copy(text)
	t.setEphemeralHint("yanked to clipboard", 2*time.Second)
}

func (t *TUI) handleSlash(cmd string) error {
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return nil
	}
	h := tuiCmdHost{t}
	switch parts[0] {
	case "/quit", "/q":
		return errQuitSentinel
	case "/clear":
		t.scroll = newScrollback(8192)
		t.markFullDirty()
		return nil
	case "/help", "/h", "/?":
		t.scroll.appendRaw(styleSystem, renderHelp())
		t.markScrollDirty()
		return nil
	case "/new":
		return cmdNew(h)
	case "/resume", "/r":
		if len(parts) == 1 {
			t.openSessionPicker()
			return nil
		}
		return cmdResume(h, parts[1:])
	case "/sessions":
		t.openSessionPicker()
		return nil
	case "/search":
		if len(parts) == 1 {
			t.openSearch()
			return nil
		}
		return cmdSearch(h, parts[1:])
	case "/fork":
		return cmdFork(h, parts[1:])
	case "/undo":
		return cmdUndo(h)
	case "/compact":
		t.scroll.appendRaw(styleSystem, "/compact — manual compaction not yet available (auto-compaction handles this)")
		t.markScrollDirty()
		return nil
	case "/model":
		if len(parts) == 1 {
			t.openModelPicker()
			return nil
		}
		return cmdModel(h, parts[1:])
	case "/providers":
		t.openProviderPicker()
		return nil
	case "/effort":
		return cmdEffort(h, parts[1:])
	case "/models":
		t.openModelPicker()
		return nil
	case "/reload":
		return cmdReload(h)
	case "/cost":
		cmdCost(h)
		return nil
	case "/btw":
		question := strings.TrimSpace(strings.TrimPrefix(cmd, parts[0]))
		if question == "" {
			t.scroll.appendRaw(styleSystem, "usage: /btw <question>")
			t.markScrollDirty()
			return nil
		}
		t.openBTW(question)
		return nil
	default:
		t.scroll.appendRaw(styleSystem, "unknown command: "+parts[0]+" (type /help)")
		t.markScrollDirty()
		return nil
	}
}

var errQuitSentinel = errors.New("quit")

// cwdOf returns the current working directory or "" on error.
func cwdOf() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

// getenv is a tiny indirection so tests can override the environment without
// touching os.Setenv across goroutines.
var getenv = os.Getenv


