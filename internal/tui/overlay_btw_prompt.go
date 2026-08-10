package tui

import "strings"

// btwPromptOverlay is a floating input opened by Ctrl+B — one line normally,
// word-wrapping across more when the question outgrows the terminal width.
// It collects a /btw side question without touching whatever the user
// already has drafted in the main input box — an alternative way to reach
// the exact same side-question flow as typing "/btw <question>" directly,
// for when clearing the draft to type "/btw" isn't an option.
type btwPromptOverlay struct {
	query    string
	onSubmit func(question string)
}

func newBTWPromptOverlay(onSubmit func(question string)) *btwPromptOverlay {
	return &btwPromptOverlay{onSubmit: onSubmit}
}

func (o *btwPromptOverlay) render(scrollRows, cols int) (int, []string) {
	if cols < 12 {
		cols = 12
	}
	label := fgCyan + bold + " btw: " + reset
	hint := dim + " · Enter ask · Esc cancel" + reset

	if o.query == "" {
		q := dim + "ask a side question — your draft stays put" + reset
		return 1, []string{truncateToWidth(label+q+hint, cols)}
	}

	// A long question must wrap like the main input instead of getting cut
	// off past the terminal edge — truncateToWidth on a single line silently
	// dropped everything past cols. Continuation lines indent under the
	// label so the wrapped question still reads as one block.
	prefixWidth := visibleWidth(label)
	wrapW := cols - prefixWidth
	if wrapW < 8 {
		wrapW = 8
	}
	wrapped := wrapWords(o.query, wrapW)
	lines := make([]string, len(wrapped))
	indent := strings.Repeat(" ", prefixWidth)
	for i, ln := range wrapped {
		prefix := label
		if i > 0 {
			prefix = indent
		}
		content := prefix + fgCyan + ln + reset
		if i == len(wrapped)-1 {
			content += hint
		}
		lines[i] = truncateToWidth(content, cols)
	}
	return 1, lines
}

func (o *btwPromptOverlay) feedKey(k Key) (handled bool, done bool, cancel bool) {
	switch {
	case k.isCtrlC():
		return true, true, true
	case k.Kind == KeyEscape:
		return true, true, true
	case k.isEnter():
		q := strings.TrimSpace(o.query)
		if q == "" {
			// Nothing typed yet — same as /btw with no question: stay open
			// instead of silently closing on a stray Enter.
			return true, false, false
		}
		if o.onSubmit != nil {
			o.onSubmit(q)
		}
		return true, true, false
	case k.Kind == KeyBackspace:
		trimOverlayFilter(&o.query)
		return true, false, false
	case k.Kind == KeyRune:
		appendOverlayFilterRune(&o.query, k.Rune)
		return true, false, false
	case k.Kind == KeyPaste:
		appendOverlayFilterText(&o.query, k.Text, nil)
		return true, false, false
	}
	return false, false, false
}
