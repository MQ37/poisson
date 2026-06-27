// Package tui implements the Poisson streaming readline REPL. It runs in raw
// terminal mode, reads keys one at a time, and renders agent output events
// (streaming text, tool calls, status bar) inline. All terminal writes go
// through a single writer to avoid interleaved output.
package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/term"
	"poisson/internal/agent"
)

// TUI is the interactive readline REPL. It owns the raw-mode terminal,
// the input line editor, history, and the rendering of agent output events.
type TUI struct {
	outputChan chan agent.OutputEvent
	history    []string
	histIdx    int
	input      string
	cursorPos  int
	agent      *agent.Agent
	sessionID  string

	// Bracketed-paste accumulation across reads.
	pasting  bool
	pasteBuf []byte

	lastCtrlC time.Time

	// Approval coordination. While an approval prompt is active it owns stdin
	// exclusively (blocking mode); the Ctrl+C poller skips reading. pollerActive
	// is true only while drainOutput runs its nonblocking poll loop.
	approvalMu   sync.Mutex
	approving    atomic.Bool
	pollerActive atomic.Bool

	// writer is where all rendered output is written. Defaults to os.Stdout.
	writer io.Writer
	// fd is the terminal file descriptor used for raw-mode toggling.
	fd int
	// oldState is the saved terminal state to restore on exit.
	oldState *term.State
}

// NewTUI constructs a TUI wired to the given agent and output channel. The
// sessionID is shown in the status bar.
func NewTUI(a *agent.Agent, sessionID string, outputChan chan agent.OutputEvent) *TUI {
	return &TUI{
		outputChan: outputChan,
		history:    []string{},
		histIdx:    -1,
		agent:      a,
		sessionID:  sessionID,
		writer:     os.Stdout,
		fd:         int(os.Stdin.Fd()),
	}
}

// Run starts the REPL loop. It puts the terminal into raw mode, reads keys,
// and dispatches: Enter submits input (→ agent.Prompt → drain outputChan),
// ↑/↓ navigates history, Tab completes slash commands, Ctrl+J inserts a
// newline, Ctrl+C exits.
// Run starts the REPL. If onReady is non-nil it is called with the live
// Approver (v2 or classic *TUI) before the session blocks, so approval
// callbacks wired into the agent can reach the running TUI.
func (t *TUI) Run(onReady func(Approver)) error {
	if os.Getenv("POISSON_TUI") != "classic" {
		v2 := newTUIv2(t.agent, t.sessionID, t.outputChan)
		if t.outputChan == nil {
			v2.output = nil
		}
		if onReady != nil {
			onReady(v2)
		}
		return v2.Run()
	}
	if onReady != nil {
		onReady(t)
	}
	oldState, err := term.MakeRaw(t.fd)
	if err != nil {
		return fmt.Errorf("enter raw mode: %w", err)
	}
	t.oldState = oldState
	t.writeString("\x1b[?2004h") // enable bracketed paste mode
	defer func() {
		t.writeString("\x1b[?2004l") // disable bracketed paste mode
		_ = term.Restore(t.fd, t.oldState)
	}()

	t.printPrompt()

	buf := make([]byte, 65536)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return err
		}
		if err := t.feed(buf[:n]); err != nil {
			if err == errQuit {
				return nil
			}
			return err
		}
	}
}

// feed processes a chunk of input bytes. It handles control keys, escape
// sequences, bracketed paste, and multi-byte UTF-8 printable text. A single
// chunk may contain many bytes (e.g. a paste), so it is processed fully and
// the line is refreshed once at the end.
func (t *TUI) feed(data []byte) error {
	// If we are mid-paste (paste spanned multiple reads), keep accumulating
	// until we find the end marker.
	if t.pasting {
		if idx := bytesIndex(data, pasteEnd); idx >= 0 {
			t.pasteBuf = append(t.pasteBuf, data[:idx]...)
			t.insertPaste(string(t.pasteBuf))
			t.pasting = false
			t.pasteBuf = nil
			t.refreshLine()
			return t.feed(data[idx+len(pasteEnd):])
		}
		t.pasteBuf = append(t.pasteBuf, data...)
		return nil
	}

	dirty := false
	i := 0
	for i < len(data) {
		b := data[i]
		switch {
		case b == 3: // Ctrl+C
			return t.handleIdleCtrlC()

		case b == 13 || b == 10: // Enter / Ctrl+M / Ctrl+J
			if dirty {
				t.refreshLine()
				dirty = false
			}
			if strings.HasSuffix(t.input, "\\") {
				t.input = strings.TrimSuffix(t.input, "\\") + "\n"
				t.cursorPos = len(t.input)
				t.writeString("\r\n  ")
				t.refreshLine()
				i++
				continue
			}
			t.writeString("\r\n")
			input := t.input
			t.input = ""
			t.cursorPos = 0
			if input == "" {
				t.printPrompt()
				i++
				continue
			}
			t.history = append(t.history, input)
			t.histIdx = len(t.history)
			if err := t.processInput(input); err != nil {
				return err
			}
			t.printPrompt()
			i++

		case b == 9: // Tab
			if strings.HasPrefix(t.input, "/") {
				completed := t.tabComplete(t.input)
				if completed != t.input {
					t.input = completed
					t.cursorPos = len(t.input)
					t.refreshLine()
					dirty = false
				}
			} else if strings.Contains(t.input, "@") {
				completed := t.tabComplete(t.input)
				if completed != t.input {
					t.input = completed
					t.cursorPos = len(t.input)
					t.refreshLine()
					dirty = false
				}
			}
			i++

		case b == 127 || b == 8: // Backspace / Ctrl+H
			if t.cursorPos > 0 {
				r := []rune(t.input)
				pos := len([]rune(t.input[:t.cursorPos]))
				if pos > 0 {
					r = append(r[:pos-1], r[pos:]...)
					t.input = string(r)
					t.cursorPos = len([]byte(string(r[:pos-1])))
				}
				dirty = true
			}
			i++

		case b == 23: // Ctrl+W — delete word backward
			t.deleteWordBackward()
			dirty = false
			i++

		case b == 21: // Ctrl+U — delete to beginning of line
			t.input = t.input[t.cursorPos:]
			t.cursorPos = 0
			dirty = true
			i++

		case b == 11: // Ctrl+K — delete to end of line
			t.input = t.input[:t.cursorPos]
			dirty = true
			i++

		case b == 27: // ESC — escape sequence, arrow key, or bracketed paste
			if dirty {
				t.refreshLine()
				dirty = false
			}
			consumed, quit, err := t.handleEscape(data[i:])
			if err != nil {
				return err
			}
			if quit {
				return nil
			}
			if consumed <= 0 {
				consumed = 1
			}
			i += consumed

		case b < 32: // other control chars — ignore
			i++

		default: // printable UTF-8 rune
			r, size := utf8.DecodeRune(data[i:])
			if r == utf8.RuneError && size <= 1 {
				i++
				continue
			}
			t.insertChar(r)
			dirty = true
			i += size
		}
	}
	if dirty {
		t.refreshLine()
	}
	return nil
}

var (
	pasteStart = []byte("\x1b[200~")
	pasteEnd   = []byte("\x1b[201~")
)

// handleEscape processes an escape sequence starting at data[0] == 27.
// Returns the number of bytes consumed, whether to quit, and any error.
func (t *TUI) handleEscape(data []byte) (int, bool, error) {
	if len(data) < 2 {
		return 1, false, nil // lone ESC
	}

	// Alt+Backspace: ESC DEL or ESC BS.
	if data[1] == 127 || data[1] == 8 {
		t.deleteWordBackward()
		return 2, false, nil
	}

	if data[1] == '[' {
		// Bracketed paste start.
		if bytesHasPrefix(data, pasteStart) {
			rest := data[len(pasteStart):]
			if idx := bytesIndex(rest, pasteEnd); idx >= 0 {
				t.insertPaste(string(rest[:idx]))
				t.refreshLine()
				return len(pasteStart) + idx + len(pasteEnd), false, t.feed(rest[idx+len(pasteEnd):])
			}
			// Paste spans multiple reads — accumulate.
			t.pasting = true
			t.pasteBuf = append([]byte{}, rest...)
			return len(data), false, nil
		}
		// Arrow keys: ESC [ A/B/C/D.
		if len(data) >= 3 {
			switch data[2] {
			case 'A': // Up
				t.navigateHistory(-1)
				t.refreshLine()
				return 3, false, nil
			case 'B': // Down
				t.navigateHistory(1)
				t.refreshLine()
				return 3, false, nil
			case 'C': // Right
				if t.cursorPos < len(t.input) {
					_, size := decodeRuneAt(t.input, t.cursorPos)
					t.cursorPos += size
					t.writeString("\x1b[C")
				}
				return 3, false, nil
			case 'D': // Left
				if t.cursorPos > 0 {
					_, size := decodeRuneBefore(t.input, t.cursorPos)
					t.cursorPos -= size
					t.writeString("\x1b[D")
				}
				return 3, false, nil
			}
		}
		// Unknown CSI — consume up to and including the final byte (0x40-0x7e).
		j := 2
		for j < len(data) && (data[j] < 0x40 || data[j] > 0x7e) {
			j++
		}
		if j < len(data) {
			j++
		}
		return j, false, nil
	}

	return 2, false, nil // ESC + other — skip both
}

// insertPaste inserts pasted text at the cursor, normalizing line endings.
func (t *TUI) insertPaste(s string) {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	t.input = t.input[:t.cursorPos] + s + t.input[t.cursorPos:]
	t.cursorPos += len(s)
}

// bytesIndex / bytesHasPrefix — small helpers to avoid importing bytes
// solely for two calls (a little copying beats a little dependency).
func bytesIndex(haystack, needle []byte) int {
	if len(needle) == 0 {
		return 0
	}
outer:
	for i := 0; i+len(needle) <= len(haystack); i++ {
		for j := 0; j < len(needle); j++ {
			if haystack[i+j] != needle[j] {
				continue outer
			}
		}
		return i
	}
	return -1
}

func bytesHasPrefix(s, prefix []byte) bool {
	if len(s) < len(prefix) {
		return false
	}
	for i := range prefix {
		if s[i] != prefix[i] {
			return false
		}
	}
	return true
}

// processInput handles one submitted line. It expands @file references,
// dispatches slash commands, or sends the input to the agent and drains the
// output channel.
func (t *TUI) processInput(input string) error {
	trimmed := strings.TrimSpace(input)
	if strings.HasPrefix(trimmed, "/") {
		return t.handleSlashCommand(trimmed)
	}

	expanded, err := expandAtFiles(input)
	if err != nil {
		t.writeString("error: " + err.Error() + "\r\n")
		return nil
	}

	// Send to agent and drain output channel concurrently. The agent's Prompt
	// is synchronous and streams events to outputChan; we run it in a goroutine
	// and drain the channel until we see a "done" event (or the goroutine
	// finishes).
	if t.agent != nil {
		t.writeString("  ⠋ thinking...\r\n")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		promptDone := make(chan error, 1)
		go func() {
			promptDone <- t.agent.PromptWithContext(ctx, expanded)
		}()
		if err := t.drainOutput(promptDone, cancel); err != nil {
			if err == errQuit {
				return err
			}
			if !errors.Is(err, context.Canceled) {
				t.writeString("error: " + err.Error() + "\r\n")
			}
			return nil
		}
		select {
		case err := <-promptDone:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.writeString("error: " + err.Error() + "\r\n")
			}
		default:
		}
	}
	return nil
}

// drainOutput reads OutputEvents until the prompt finishes. If cancel is not
// nil, Ctrl+C cancels the running prompt; a second Ctrl+C exits the CLI.
func (t *TUI) drainOutput(promptDone <-chan error, cancel context.CancelFunc) error {
	if t.outputChan == nil {
		return <-promptDone
	}

	var tick <-chan time.Time
	var ticker *time.Ticker
	if cancel != nil {
		if err := syscall.SetNonblock(t.fd, true); err == nil {
			t.pollerActive.Store(true)
			defer func() {
				t.pollerActive.Store(false)
				syscall.SetNonblock(t.fd, false)
			}()
			ticker = time.NewTicker(50 * time.Millisecond)
			tick = ticker.C
			defer ticker.Stop()
		}
	}

	cancelled := false
	for {
		select {
		case ev, ok := <-t.outputChan:
			if !ok {
				return <-promptDone
			}
			if ev.Type == agent.OutputDone {
				t.writeString("\r\n")
				return nil
			}
			t.renderEvent(ev)
		case err := <-promptDone:
			if drainErr := t.drainBufferedOutput(); drainErr != nil {
				return drainErr
			}
			return err
		case <-tick:
			// An active approval prompt owns stdin; don't steal its keypress.
			if t.approving.Load() {
				continue
			}
			var err error
			cancelled, err = t.pollRunningCtrlC(cancel, cancelled)
			if err != nil {
				return err
			}
		}
	}
}

// Approve renders an approval prompt for a dangerous bash command and reads a
// single keypress. It takes stdin exclusively in blocking mode for the
// duration so the Ctrl+C poller (which runs stdin nonblocking) cannot steal
// the keypress. 'a'/'y' allow; anything else (incl. Ctrl+C) denies.
// Always shows $ command and labeled Purpose: line (PR-24).
func (t *TUI) Approve(command, description string) bool {
	t.approvalMu.Lock()
	defer t.approvalMu.Unlock()

	t.approving.Store(true)
	defer t.approving.Store(false)

	if t.pollerActive.Load() {
		syscall.SetNonblock(t.fd, false)
		defer syscall.SetNonblock(t.fd, true)
	}

	purpose := description
	if purpose == "" {
		purpose = "(no description provided)"
	}
	t.writeString("\r\n" + fgYellow + bold + "⚠ approval required" + reset + "\r\n")
	t.writeString("  $ " + command + "\r\n")
	t.writeString("  " + dim + "Purpose: " + purpose + reset + "\r\n")
	t.writeString("  [a]llow  [d]eny: ")

	buf := make([]byte, 1)
	n, err := os.Stdin.Read(buf)
	if err != nil || n == 0 {
		t.writeString("\r\n")
		return false
	}
	allowed := buf[0] == 'a' || buf[0] == 'A' || buf[0] == 'y' || buf[0] == 'Y'
	if allowed {
		t.writeString("allow\r\n")
	} else {
		t.writeString("deny\r\n")
	}
	return allowed
}

func (t *TUI) drainBufferedOutput() error {
	for {
		select {
		case ev, ok := <-t.outputChan:
			if !ok {
				return nil
			}
			if ev.Type == agent.OutputDone {
				t.writeString("\r\n")
				return nil
			}
			t.renderEvent(ev)
		default:
			return nil
		}
	}
}

func (t *TUI) pollRunningCtrlC(cancel context.CancelFunc, cancelled bool) (bool, error) {
	buf := make([]byte, 64)
	for {
		n, err := syscall.Read(t.fd, buf)
		if n > 0 {
			for _, b := range buf[:n] {
				if b != 3 {
					continue
				}
				if cancelled {
					t.writeString("\r\n")
					return cancelled, errQuit
				}
				cancel()
				cancelled = true
				t.writeString("\r\n  cancelled — Ctrl+C again to exit\r\n")
			}
		}
		if err == nil {
			continue
		}
		if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK {
			return cancelled, nil
		}
		if err == syscall.EINTR {
			continue
		}
		return cancelled, nil
	}
}

// navigateHistory moves through the history list. dir is -1 for up, +1 for
// down. At the boundaries it clamps (top stays at oldest, bottom restores the
// in-progress input).
func (t *TUI) navigateHistory(dir int) {
	if len(t.history) == 0 {
		return
	}
	if t.histIdx == -1 {
		t.histIdx = len(t.history)
	}
	t.histIdx += dir
	if t.histIdx < 0 {
		t.histIdx = 0
	}
	if t.histIdx >= len(t.history) {
		t.histIdx = len(t.history)
		t.input = ""
		t.cursorPos = 0
		return
	}
	t.input = t.history[t.histIdx]
	t.cursorPos = len(t.input)
}

// tabComplete does prefix completion for slash commands and @file references.
// On the first call it expands to the longest common prefix; on a second
// consecutive Tab call it cycles through candidates (numeric suffix appended).
func (t *TUI) tabComplete(input string) string {
	cwd, _ := os.Getwd()
	if strings.HasPrefix(strings.TrimSpace(input), "/") {
		if space := strings.IndexByte(input, ' '); space >= 0 {
			return input
		}
		cands := matchSlash(input)
		if len(cands) == 1 {
			return cands[0] + " "
		}
		if len(cands) > 1 {
			if lcp := commonPrefixCands(cands); len(lcp) > len(input) {
				return lcp
			}
		}
		return input
	}
	if atIdx := strings.LastIndexByte(input, '@'); atIdx >= 0 {
		body := input[atIdx:]
		cands := matchAtFile("@"+body, cwd)
		if len(cands) == 1 {
			return input[:atIdx] + cands[0]
		}
		if len(cands) > 1 {
			if lcp := commonPrefixCands(cands); len(lcp) > len(body)+1 {
				return input[:atIdx] + lcp
			}
		}
	}
	return input
}

func commonPrefix(a, b string) string {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return a[:i]
		}
	}
	return a[:n]
}

// handleSlashCommand parses and dispatches a slash command.
func (t *TUI) handleSlashCommand(cmd string) error {
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return nil
	}
	h := tuiCmdHost{t}
	switch parts[0] {
	case "/quit", "/q":
		t.writeString("bye.\r\n")
		return errQuit
	case "/clear":
		t.writeString("\x1b[2J\x1b[H")
		return nil
	case "/help", "/h", "/?":
		t.writeString(renderHelp())
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
		t.writeString("/compact — manual compaction not yet available (auto-compaction handles this)\r\n")
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
		t.writeString("unknown command: " + parts[0] + " (type /help)\r\n")
		return nil
	}
}

// errQuit is a sentinel returned by handleSlashCommand via processInput to
// tell Run to exit. Run checks for it after processInput.
var errQuit = fmt.Errorf("quit")

const ctrlCExitWindow = 2 * time.Second

func (t *TUI) handleIdleCtrlC() error {
	if t.input != "" {
		t.input = ""
		t.cursorPos = 0
		t.lastCtrlC = time.Time{}
		t.refreshLine()
		return nil
	}
	now := time.Now()
	if !t.lastCtrlC.IsZero() && now.Sub(t.lastCtrlC) <= ctrlCExitWindow {
		t.writeString("\r\n")
		return errQuit
	}
	t.lastCtrlC = now
	t.writeString("\r\nCtrl+C again to exit\r\n")
	t.printPrompt()
	return nil
}

const promptText = "poisson> "

// printPrompt writes the prompt.
func (t *TUI) printPrompt() {
	t.writeString(promptText)
}

// refreshLine redraws the current input line. It never prints past the terminal
// width; wrapped prompts cannot be reliably cleared with a one-line editor.
func (t *TUI) refreshLine() {
	maxCols := t.inputViewportCols()
	visible, cursorCol := visibleInput(t.input, t.cursorPos, maxCols)

	t.writeString("\x1b[2K\r")
	t.writeString(promptText)
	t.writeString(visible)
	if back := runeCount(visible) - cursorCol; back > 0 {
		t.writeString(fmt.Sprintf("\x1b[%dD", back))
	}
}

func (t *TUI) inputViewportCols() int {
	width, _, err := term.GetSize(t.fd)
	if err != nil || width <= len(promptText)+2 {
		width = 80
	}
	cols := width - len(promptText) - 1 // keep one column free to avoid autowrap
	if cols < 1 {
		return 1
	}
	return cols
}

func visibleInput(input string, cursorPos, maxCols int) (string, int) {
	if maxCols <= 0 {
		return "", 0
	}
	if cursorPos < 0 {
		cursorPos = 0
	}
	if cursorPos > len(input) {
		cursorPos = len(input)
	}

	runes := []rune(input)
	cursorRune := len([]rune(input[:cursorPos]))
	for i, r := range runes {
		switch {
		case r == '\n' || r == '\r':
			runes[i] = '↵'
		case r == '\t':
			runes[i] = ' '
		case r < 32 || r == 127:
			runes[i] = '?'
		}
	}
	if len(runes) <= maxCols {
		return string(runes), cursorRune
	}
	if maxCols == 1 {
		if cursorRune == 0 {
			return "…", 0
		}
		return "…", 1
	}

	slots := maxCols - 1
	if cursorRune <= slots {
		end := slots
		if end > len(runes) {
			end = len(runes)
		}
		return string(runes[:end]) + "…", cursorRune
	}

	start := cursorRune - slots
	end := start + slots
	if end > len(runes) {
		end = len(runes)
	}
	return "…" + string(runes[start:end]), 1 + cursorRune - start
}

func runeCount(s string) int { return len([]rune(s)) }

func (t *TUI) insertChar(r rune) {
	s := string(r)
	t.input = t.input[:t.cursorPos] + s + t.input[t.cursorPos:]
	t.cursorPos += len(s)
}

func (t *TUI) writeString(s string) {
	if t.writer != nil {
		_, _ = io.WriteString(t.writer, s)
	}
}

// decodeRuneAt decodes the rune at byte position pos in s, returning the
// rune and its byte size.
func decodeRuneAt(s string, pos int) (rune, int) {
	return []rune(s[pos:])[0], len(string([]rune(s[pos:])[0]))
}

// decodeRuneBefore decodes the rune immediately before byte position pos.
func decodeRuneBefore(s string, pos int) (rune, int) {
	r := []rune(s[:pos])
	return r[len(r)-1], len(string(r[len(r)-1]))
}

// --- @file expansion ---

var atFileRe = regexp.MustCompile(`@([^\s@]+)`)

// expandAtFiles replaces @path references in input with the file's contents
// inlined as a fenced code block. A nonexistent path produces an error.
func expandAtFiles(input string) (string, error) {
	var firstErr error
	result := atFileRe.ReplaceAllStringFunc(input, func(match string) string {
		path := match[1:] // strip '@'
		data, err := os.ReadFile(path)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("read @%s: %w", path, err)
			}
			return match
		}
		// Detect a fence length that doesn't appear in the file contents.
		fence := "```"
		for strings.Contains(string(data), fence) {
			fence += "`"
		}
		return fence + "\n" + string(data) + "\n" + fence
	})
	if firstErr != nil {
		return "", firstErr
	}
	return result, nil
}

// renderHelp returns the help text listing all slash commands.
func renderHelp() string {
	var b strings.Builder
	b.WriteString("Slash commands:\r\n")
	b.WriteString("  /help        Show this help\r\n")
	b.WriteString("  /quit        Exit Poisson\r\n")
	b.WriteString("  /clear       Clear the screen\r\n")
	b.WriteString("  /new         Start a new session\r\n")
	b.WriteString("  /resume <id> Resume a session\r\n")
	b.WriteString("  /sessions    List sessions\r\n")
	b.WriteString("  /search <q>  Search across sessions\r\n")
	b.WriteString("  /fork [seq]  Fork the current session\r\n")
	b.WriteString("  /undo        Undo the last turn\r\n")
	b.WriteString("  /compact     Compact context now\r\n")
	b.WriteString("  /model <m>   Switch provider/model (e.g. ollama/glm-5.2:cloud)\r\n")
	b.WriteString("  /providers  List available providers\r\n")
	b.WriteString("  /effort <l>   Set thinking effort (low|medium|high|xhigh|max)\r\n")
	b.WriteString("  /models     List models from current provider\r\n")
	b.WriteString("  /reload      Reload config and skills\r\n")
	b.WriteString("  /cost        Show session cost\r\n")
	b.WriteString("  /btw <q>     Side question in floating box (Esc close/cancel)\r\n")
	return b.String()
}

// deleteWordBackward deletes the word before the cursor, similar to
// Alt+Backspace or Ctrl+W in readline. Skips trailing whitespace, then
// deletes back to the previous word boundary.
func (t *TUI) deleteWordBackward() {
	if t.cursorPos == 0 {
		return
	}
	// Work in runes for correctness.
	r := []rune(t.input)
	pos := len([]rune(t.input[:t.cursorPos]))
	if pos == 0 {
		return
	}
	// Skip whitespace before cursor.
	for pos > 0 && (r[pos-1] == ' ' || r[pos-1] == '\t' || r[pos-1] == '\n') {
		pos--
	}
	// Delete back to start of word (non-whitespace chars).
	for pos > 0 && r[pos-1] != ' ' && r[pos-1] != '\t' && r[pos-1] != '\n' {
		pos--
	}
	// r[pos:t.cursorPos_runes] is the deleted region.
	cursorRuneIdx := len([]rune(t.input[:t.cursorPos]))
	r = append(r[:pos], r[cursorRuneIdx:]...)
	t.input = string(r)
	t.cursorPos = len([]byte(string(r[:pos])))
	t.refreshLine()
}
