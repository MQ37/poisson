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

// tuiV2 is the split-screen alt-screen TUI. The classic readline TUI stays in
// tui.go as the fallback (POISSON_TUI=classic).
type tuiV2 struct {
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
	scrollRows int // rows allotted to scrollback (= rows - statusRows - inputRows)
	inputRows  int // rows for multi-line input (3)
	statusRows int // rows for status bar (2)

	// Region content.
	scroll *scrollback
	editor *editor
	status StatusSnapshot
	dirty  dirtyTracker

	renderFrame    int
	activeTools    int
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
	completion *completion

	// Approval coordination. The input goroutine is the sole stdin reader;
	// when approval is pending it routes the answer through this channel
	// instead of feeding it to the editor.
	approvalMu     sync.Mutex
	approving      atomic.Bool
	approvalAnswer chan bool

	// Submission signaling.
	lastCtrlC time.Time

	// cancelRun is set while an agent prompt is in flight. The input goroutine
	// uses it to cancel a running request on Ctrl+C.
	cancelRun context.CancelFunc
	cancelMu  sync.Mutex
}

// newTUIv2 constructs the split-screen TUI.
func newTUIv2(a *agent.Agent, sessionID string, outputChan chan agent.OutputEvent) *tuiV2 {
	return &tuiV2{
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
			ShowTokens: true,
			ShowCost:   true,
		},
		dirty:          newDirtyTracker(),
		inputRows:      3,
		statusRows:     2,
		lastInputRows:  3,
		done:           make(chan struct{}),
		approvalAnswer: make(chan bool),
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
func (t *tuiV2) inputHeight(width int) int {
	n := totalVisualLines(t.editor, width) + 3 // +1 header, +1 separator, +1 hint
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
func (t *tuiV2) Run() error {
	oldState, err := term.MakeRaw(t.fd)
	if err != nil {
		return fmt.Errorf("enter raw mode: %w", err)
	}
	t.oldState = oldState
	t.recomputeLayout()

	// Wire terminal mode.
	t.writeRaw(altScreenOn + hideCursor + bracketedOn + kittyKbOn)
	t.installResize()

	// Restore terminal on any exit path — including panic — so the user's
	// shell isn't left in raw alt-screen with kitty keyboard enabled.
	defer func() {
		t.stopped.Store(true)
		t.writeRaw(kittyKbOff + bracketedOff + showCursor + altScreenOff)
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
				_ = fmt.Sprintf("render panic: %v", r)
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
			// If an approval prompt is active, route the answer to the
			// approval channel instead of feeding it to the editor.
			// This avoids the race where Approve and the input goroutine
			// both read from stdin.
			if t.approving.Load() {
				allowed, ok := approvalKeyAllowed(buf[:n])
				if ok {
					t.approvalAnswer <- allowed
				}
				continue
			}
			quit, err := t.feed(decodeKittyKeys(buf[:n]))
			if err != nil {
				t.appendError(err)
				continue
			}
			if quit {
				return
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

// feed handles one chunk of input bytes. It returns (quit, error). If quit is
// true, the input goroutine should exit. It must be called from the input
// goroutine only.
func (t *tuiV2) feed(data []byte) (bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Ensure wrapWidth is set so editor movements use the correct grid.
	if t.editor.wrapWidth < 1 && t.cols > 0 {
		w := t.cols - 1
		if w < 1 {
			w = 1
		}
		t.editor.wrapWidth = w
	}

	// While a prompt is running, only Ctrl+C is meaningful.
	if t.running() {
		if containsCtrlC(data) {
			t.cancelMu.Lock()
			cancel := t.cancelRun
			t.cancelMu.Unlock()
			if cancel != nil {
				cancel()
				t.lastCtrlC = time.Now()
				t.status.Hint = "cancelled — Ctrl+C again to exit"
				t.dirty.markStatus()
			}
		}
		return false, nil
	}

	// If mid-paste or starting a bracketed paste, bypass all key interception
	// (Tab/Enter/Esc/arrows) so pasted bytes don't trigger completions or
	// submissions. The editor handles paste accumulation.
	if t.editor.paste || (len(data) >= 6 && data[0] == 27 && data[1] == '[' && data[2] == '2' && data[3] == '0' && data[4] == '0' && data[5] == '~') {
		return t.processEditor(data)
	}

	// Scrollback navigation.
	if indexOf(data, []byte("\x1b[5~")) >= 0 { // PageUp
		t.scroll.scrollUp(t.scrollRows, t.scrollRows)
		t.markScrollDirty()
		return false, nil
	}
	if indexOf(data, []byte("\x1b[6~")) >= 0 { // PageDown
		t.scroll.scrollDown(t.scrollRows)
		t.markScrollDirty()
		return false, nil
	}

	// Tab: trigger or cycle/accept completion. Don't return early — other
	// bytes in this chunk still need processing by editor.feed (Tab itself is
	// ignored by the editor).
	for _, b := range data {
		if b == 9 {
			t.handleTab()
			t.markInputDirty()
		}
	}

	// Enter accepts the current completion selection when the dropdown is open.
	if !t.completion.empty() && t.completion.idx >= 0 && containsSubmitKey(data) {
		t.applyCompletion(t.completion.cands[t.completion.idx])
		t.completion = nil
		t.markInputDirty()
		return false, nil
	}

	// Escape: dismiss the completion dropdown if open.
	for _, b := range data {
		if b == 27 {
			if t.completion != nil && !t.completion.empty() && !hasCSI(data) {
				t.completion = nil
				t.markInputDirty()
				return false, nil
			}
		}
	}

	// Arrow up/down cycle through completion while it's open.
	if !t.completion.empty() {
		for i, b := range data {
			if b == 27 && i+2 < len(data) && data[i+1] == '[' {
				switch data[i+2] {
				case 'A':
					t.completion.cycle(-1)
					t.markInputDirty()
					return false, nil
				case 'B':
					t.completion.cycle(+1)
					t.markInputDirty()
					return false, nil
				}
			}
		}
	}

	// Ctrl+C: clear editor / exit when idle.
	if containsCtrlC(data) {
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
			t.status.Hint = "Ctrl+C again to exit"
			t.dirty.markStatus()
		}
		return false, nil
	}

	// History navigation (Ctrl+P / Ctrl+N) when no completion dropdown is open.
	if t.completion.empty() {
		for _, b := range data {
			if b == 16 {
				t.navigateHistory(-1)
				t.markInputDirty()
				return false, nil
			}
			if b == 14 {
				t.navigateHistory(1)
				t.markInputDirty()
				return false, nil
			}
		}
	}

	return t.processEditor(data)
}

// processEditor feeds data to the editor and handles the result (submit/quit).
func (t *tuiV2) processEditor(data []byte) (bool, error) {
	submitted, quit := t.editor.feed(data)
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
func (t *tuiV2) handleTab() bool {
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
func (t *tuiV2) refreshCompletion() {
	cwd, _ := os.Getwd()
	line := t.editor.lines[t.editor.row]
	prefix, _ := splitPrefix(line, t.editor.col)
	var cands []string
	var kind completionKind
	switch {
	case strings.HasPrefix(strings.TrimSpace(line), "/") && !strings.ContainsAny(prefix, " \t"):
		cands = matchSlash(line)
		if len(cands) > 0 {
			kind = completionSlash
		}
	case strings.ContainsRune(prefix, '@'):
		atIdx := strings.LastIndexByte(prefix, '@')
		body := prefix[atIdx:]
		cands = matchAtFile(body, cwd)
		if len(cands) > 0 {
			kind = completionAtFile
		}
	}
	if len(cands) == 0 {
		t.completion = nil
		return
	}
	t.completion = &completion{kind: kind, prefix: prefix, cands: cands, idx: -1}
}

// splitPrefix returns (text-before-cursor-on-this-row, partial token at cursor).
func splitPrefix(line string, col int) (string, string) {
	if col > len(line) {
		col = len(line)
	}
	runes := []rune(line)
	colRune := col
	if colRune > len(runes) {
		colRune = len(runes)
	}
	head := string(runes[:colRune])
	i := len(head) - 1
	for i >= 0 && head[i] != ' ' && head[i] != '\t' && head[i] != '\n' {
		i--
	}
	return head, head[i+1:]
}

// acceptCompletion inserts the selected candidate at the cursor, replacing
// the partial token.
func (t *tuiV2) acceptCompletion() {
	if t.completion == nil || t.completion.empty() || t.completion.idx < 0 {
		return
	}
	t.applyCompletion(t.completion.cands[t.completion.idx])
	t.refreshCompletion()
}

// applyCompletion replaces the partial token before the cursor with s.
func (t *tuiV2) applyCompletion(s string) {
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

func (t *tuiV2) submit(text string) error {
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
	t.cancelRun = cancel
	t.cancelMu.Unlock()

	go func() {
		defer func() {
			t.cancelMu.Lock()
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
func (t *tuiV2) handleEvent(ev agent.OutputEvent) {
	switch ev.Type {
	case agent.OutputText:
		t.scroll.append(StyledLine{Style: styleAssistant, Text: ev.Text})
	case agent.OutputThinking:
		t.scroll.append(StyledLine{Style: styleThinking, Text: ev.Text})
	case agent.OutputToolStart:
		var b strings.Builder
		b.WriteString(fmt.Sprintf("\n  [%s] %s\n  %s working...", ev.ToolName,
			toolInputPreview(ev.ToolName, ev.ToolInput), spinnerChar(t.renderFrame)))
		t.scroll.appendRaw(styleToolStart, b.String())
	case agent.OutputToolResult:
		var b strings.Builder
		if ev.ToolError != "" {
			b.WriteString(fmt.Sprintf("  ✗ %s", previewText(ev.ToolError, 400)))
		} else {
			b.WriteString(fmt.Sprintf("  ✓ %s", toolResultPreview(ev.ToolName, ev.ToolResultContent)))
		}
		t.scroll.appendRaw(styleToolResult, b.String())
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
func (t *tuiV2) Approve(command, description string) bool {
	t.approvalMu.Lock()
	defer t.approvalMu.Unlock()

	t.approving.Store(true)
	defer t.approving.Store(false)

	t.mu.Lock()
	t.activeOverlay = newApprovalOverlay(command, description)
	t.dirty.markFull()
	t.mu.Unlock()
	t.paint(t.dirty.consume())

	var allowed bool
	select {
	case allowed = <-t.approvalAnswer:
	case <-t.done:
		t.mu.Lock()
		t.activeOverlay = nil
		t.mu.Unlock()
		return false
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
func (t *tuiV2) navigateHistory(dir int) {
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
func (t *tuiV2) running() bool { return t.status.Thinking }

// --- Layout ---

func (t *tuiV2) recomputeLayout() {
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
	t.statusRows = 2
	t.inputRows = t.inputHeight(wrapWidth)
	t.scrollRows = h - t.inputRows - t.statusRows
	if t.scrollRows < 3 {
		t.scrollRows = 3
	}
}

func (t *tuiV2) installResize() {
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

func (t *tuiV2) renderCompletion(c *completion) string {
	var b strings.Builder
	header := fmt.Sprintf(" %s (%d) ", prefixName(c.kind), len(c.cands))
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

func (t *tuiV2) renderInputHeader() string {
	effort := t.agent.Effort()
	if effort == "" {
		return ""
	}
	txt := fgYellow + bold + effort + reset
	gap := t.cols - visibleWidth(txt)
	if gap < 0 {
		gap = 0
	}
	return strings.Repeat(" ", gap) + txt
}

func (t *tuiV2) renderInputScreenRow(lineIdx int, screenLines []string, sr, sc int) string {
	if lineIdx >= len(screenLines) {
		return ""
	}
	line := screenLines[lineIdx]
	runes := []rune(line)
	if lineIdx != sr {
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
	b.WriteString(" ")
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

func (t *tuiV2) renderHintLine() string {
	hint := "Enter submit · Shift+Enter newline · ↑/↓ move · Ctrl+P/N history · Ctrl+D exit"
	return dim + hint + reset
}

// appendErrorLocked writes an error to the scrollback. The caller MUST hold
// t.mu (e.g. feed/submit/processEditor, which run under the lock).
func (t *tuiV2) appendErrorLocked(err error) {
	t.scroll.appendRaw(styleError, "error: "+err.Error())
	t.markScrollDirty()
}

// appendError writes an error to the scrollback, taking t.mu. Call only when
// NOT already holding the lock (e.g. the input goroutine after feed returns).
func (t *tuiV2) appendError(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.appendErrorLocked(err)
}

func (t *tuiV2) writeRaw(s string) {
	if t.writer != nil {
		_, _ = t.writer.Write([]byte(s))
	}
}

func (t *tuiV2) handleSlash(cmd string) error {
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return nil
	}
	h := v2CmdHost{t}
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
		return cmdResume(h, parts[1:])
	case "/sessions":
		cmdSessions(h)
		return nil
	case "/search":
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
		return cmdModel(h, parts[1:])
	case "/providers":
		cmdProviders(h)
		return nil
	case "/effort":
		return cmdEffort(h, parts[1:])
	case "/models":
		return cmdModels(h)
	case "/reload":
		return cmdReload(h)
	case "/cost":
		cmdCost(h)
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

// TUIv2Handle is the public alias used by cmd/px/main.go so the approval
// callback can dispatch to whichever TUI is active.
type TUIv2Handle = *tuiV2
