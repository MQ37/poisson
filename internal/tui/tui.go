// Package tui implements the Poisson streaming readline REPL. It runs in raw
// terminal mode, reads keys one at a time, and renders agent output events
// (streaming text, tool calls, status bar) inline. All terminal writes go
// through a single writer to avoid interleaved output.
package tui

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
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
func (t *TUI) Run() error {
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
			t.writeString("\r\n")
			return errQuit

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
				completed := t.slashComplete(t.input)
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
		promptDone := make(chan error, 1)
		go func() {
			promptDone <- t.agent.Prompt(expanded)
		}()
		t.drainOutput(promptDone)
		select {
		case err := <-promptDone:
			if err != nil {
				t.writeString("error: " + err.Error() + "\r\n")
			}
		default:
		}
	}
	return nil
}

// drainOutput reads OutputEvents from the channel until a "done" event is
// received (or promptDone signals that agent.Prompt has returned). Each event
// is rendered.
func (t *TUI) drainOutput(promptDone <-chan error) {
	if t.outputChan == nil {
		<-promptDone // wait for the goroutine
		return
	}
	for {
		select {
		case ev, ok := <-t.outputChan:
			if !ok {
				<-promptDone
				return
			}
			if ev.Type == agent.OutputDone {
				t.writeString("\r\n")
				return
			}
			t.renderEvent(ev)
		case <-promptDone:
			// Prompt returned; drain any remaining buffered events.
			for {
				select {
				case ev, ok := <-t.outputChan:
					if !ok {
						return
					}
					if ev.Type == agent.OutputDone {
						t.writeString("\r\n")
						return
					}
					t.renderEvent(ev)
				default:
					return
				}
			}
		}
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

// slashComplete does prefix completion for slash commands.
func (t *TUI) slashComplete(input string) string {
	commands := []string{
		"/quit", "/clear", "/help", "/new", "/resume", "/sessions",
		"/search", "/fork", "/undo", "/compact", "/model", "/effort", "/models", "/providers", "/reload", "/cost",
	}
	space := strings.IndexByte(input, ' ')
	if space >= 0 {
		return input // only complete the command token
	}
	var matches []string
	for _, c := range commands {
		if strings.HasPrefix(c, input) {
			matches = append(matches, c)
		}
	}
	if len(matches) == 1 {
		return matches[0] + " "
	}
	if len(matches) > 1 {
		// find longest common prefix
		lcp := matches[0]
		for _, m := range matches[1:] {
			lcp = commonPrefix(lcp, m)
		}
		return lcp
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
		return t.cmdNew()
	case "/resume", "/r":
		return t.cmdResume(parts[1:])
	case "/sessions":
		return t.cmdSessions()
	case "/search":
		return t.cmdSearch(parts[1:])
	case "/fork":
		return t.cmdFork(parts[1:])
	case "/undo":
		return t.cmdUndo()
	case "/compact":
		t.writeString("/compact — manual compaction not yet available (auto-compaction handles this)\r\n")
		return nil
	case "/model":
		return t.cmdModel(parts[1:])
	case "/providers":
		return t.cmdProviders()
	case "/effort":
		return t.cmdEffort(parts[1:])
	case "/models":
		return t.cmdModels()
	case "/reload":
		return t.cmdReload()
	case "/cost":
		return t.cmdCost()
	default:
		t.writeString("unknown command: " + parts[0] + " (type /help)\r\n")
		return nil
	}
}

// errQuit is a sentinel returned by handleSlashCommand via processInput to
// tell Run to exit. Run checks for it after processInput.
var errQuit = fmt.Errorf("quit")

// printPrompt writes the "poisson> " prompt.
func (t *TUI) printPrompt() {
	t.writeString("poisson> ")
}

// refreshLine redraws the current input line (used after edits).
func (t *TUI) refreshLine() {
	// Clear from cursor to end of line, move to line start, reprint prompt + input
	t.writeString("\x1b[2K\r")
	t.writeString("poisson> ")
	t.writeString(t.input)
	// Move cursor back to the right position
	if t.cursorPos < len(t.input) {
		back := len(t.input) - t.cursorPos
		t.writeString(fmt.Sprintf("\x1b[%dD", back))
	}
}

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
