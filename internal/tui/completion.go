package tui

import (
	"os"
	"strings"
)

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
