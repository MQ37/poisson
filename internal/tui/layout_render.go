package tui

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
)

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
	base := "Tab:conv · Enter:send · ↑↓:history · Ctrl+Y:yank last reply · Ctrl+F:find · Ctrl+P:palette · Ctrl+S:sessions · Ctrl+M:model · Ctrl+D:exit"
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

// yankClipboardLocked copies yankText to the system clipboard. Caller must hold t.mu.
func (t *TUI) yankClipboardLocked() {
	text := t.scroll.yankText()
	if text == "" {
		t.setEphemeralHintLocked("nothing to yank", 2*time.Second)
		return
	}
	if err := osc52Copy(text); err != nil {
		t.setEphemeralHintLocked("clipboard unavailable", 2*time.Second)
		return
	}
	t.setEphemeralHintLocked("yanked to clipboard", 2*time.Second)
}

func (t *TUI) yankClipboard() {
	t.mu.Lock()
	t.yankClipboardLocked()
	t.mu.Unlock()
}
