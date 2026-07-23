package tui

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mq37/poisson/internal/agent"
	"golang.org/x/term"
)

// --- Layout ---

func (t *TUI) recomputeLayout() {
	t.mu.Lock()
	defer t.mu.Unlock()
	w, h, err := term.GetSize(t.fd)
	if err != nil {
		w, h = 80, 24
	}
	if w < 20 {
		w = 20
	}
	if h < 8 {
		h = 8
	}
	t.rows = h
	t.cols = w
	wrapWidth := inputWrapWidth(w)
	t.editor.wrapWidth = wrapWidth
	t.headerRows = 1
	t.inputRows = t.inputHeight(wrapWidth)
	t.scrollRows = h - t.headerRows - t.inputRows
	if t.scrollRows < 3 {
		t.scrollRows = 3
	}
	t.scroll.clampScrollOffset(t.convScrollRows(), t.contentWidth())
	t.syncConvUserIdxFromScrollLocked()
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

// paintApprovalInputRegion fills the entire input region with an opaque approval
// panel (replacing the normal prompt editor).
func (t *TUI) paintApprovalInputRegion(b *strings.Builder, lay layoutSnapshot, o *approvalOverlay) {
	lines := o.renderInputPanel(t.inputRows, t.cols)
	for i := 0; i < t.inputRows; i++ {
		row := lay.inputTop + i
		b.WriteString(cup(row, 1))
		b.WriteString(clearLine())
		if i < len(lines) {
			b.WriteString(truncateToWidth(lines[i], t.cols))
		} else {
			b.WriteString(fillWidthBG(approvalPanelBG(), "", t.cols))
		}
	}
	endRow := lay.inputTop + t.inputRows
	for r := endRow; r <= t.rows; r++ {
		b.WriteString(cup(r, 1))
		b.WriteString(clearLine())
	}
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
	maxWidth := t.cols
	if maxWidth < 1 {
		maxWidth = 1
	}
	if lineIdx != sr {
		if lineIdx == 0 {
			return truncateToWidth(prompt+string(runes), maxWidth)
		}
		return truncateToWidth(" "+string(runes), maxWidth)
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
	if lineIdx > 0 {
		b.WriteString(" ")
	}
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
	return truncateToWidth(b.String(), maxWidth)
}

// renderHintLine renders the bottom-most row of the input region: keybinding
// hints on the left, the current approval mode (Fast/Paranoid, Shift+Tab to
// toggle) right-aligned to width. Falls back to the left part alone when
// there isn't room for both — the hint must never be sacrificed to make
// space for the mode tag.
func (t *TUI) renderHintLine(width int) string {
	left := t.hintLineLeft()
	if width < 1 {
		return left
	}
	right := t.approvalModeTag()
	gap := width - visibleWidth(left) - visibleWidth(right)
	if gap < 1 {
		return left
	}
	return left + strings.Repeat(" ", gap) + right
}

func (t *TUI) hintLineLeft() string {
	if t.focusRegion == focusConv {
		return dim + "Tab:input · PgUp/Dn:scroll · Shift+←/→:prompts · Ctrl+E:tool" + reset
	}
	base := "Tab:conv · Enter:send · Ctrl+V:image · Ctrl+F:find · Ctrl+P:palette · Ctrl+L:effort · Ctrl+T:think · Ctrl+E:tool · Ctrl+Y:copy · Ctrl+G:expedite · Ctrl+C:clear"
	if t.running() {
		base = "Enter:queue message · Esc:cancel · Ctrl+G:expedite · Ctrl+T:think · Ctrl+E:tool"
	}
	return dim + base + reset
}

// renderInfoLine renders the ephemeral status.Hint (e.g. "Copied N lines to
// clipboard", a mode-toggle notice, ...) on its own row, directly above the
// keybinding hint row — kept off that already-long line so it doesn't push
// the mode tag (or the keybindings themselves) past the terminal width.
// Empty when there's no active hint (prepareLayout then omits the row
// entirely — see infoRow in render_v2.go).
func (t *TUI) renderInfoLine() string {
	if t.status.Hint == "" {
		return ""
	}
	return dim + t.status.Hint + reset
}

// approvalModeTag renders the current bash-approval mode for the status
// line's bottom-right corner. Paranoid is called out in yellow — it means
// every command, however trivial, stops for a human; Fast is dim/green since
// it's the default, low-friction path.
func (t *TUI) approvalModeTag() string {
	if t.status.ApprovalMode == agent.ApprovalModeParanoid {
		return fgYellow + "⇥ PARANOID" + reset
	}
	return dim + fgGreen + "⇥ FAST" + reset
}

// scrollByDelta scrolls the scrollback viewport. Caller must hold t.mu.
func (t *TUI) scrollByDelta(delta int) {
	if delta > 0 {
		t.scroll.scrollUp(delta)
	} else if delta < 0 {
		t.scroll.scrollDown(-delta)
	}
	t.scroll.clampScrollOffset(t.convScrollRows(), t.contentWidth())
	t.syncConvUserIdxFromScrollLocked()
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
// t.mu (e.g. feed/submit, which run under the lock).
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
