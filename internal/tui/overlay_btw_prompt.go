package tui

import "strings"

// btwPromptOverlay is a floating single-line input opened by Ctrl+B. It
// collects a /btw side question without touching whatever the user already
// has drafted in the main input box — an alternative way to reach the exact
// same side-question flow as typing "/btw <question>" directly, for when
// clearing the draft to type "/btw" isn't an option.
type btwPromptOverlay struct {
	query    string
	onSubmit func(question string)
}

func newBTWPromptOverlay(onSubmit func(question string)) *btwPromptOverlay {
	return &btwPromptOverlay{onSubmit: onSubmit}
}

func (o *btwPromptOverlay) render(scrollRows, cols int) (int, []string) {
	q := o.query
	if q == "" {
		q = dim + "ask a side question — your draft stays put" + reset
	} else {
		q = fgCyan + q + reset
	}
	label := fgCyan + bold + " btw: " + reset + q
	hint := dim + " · Enter ask · Esc cancel" + reset
	return 1, []string{truncateToWidth(label+hint, cols)}
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
