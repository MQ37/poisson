package tui

import (
	"context"
	"strings"
	"sync"
)

// btwOverlay is a full-width, scrollable side-question panel (/btw) rendered
// like the bash-approval overlay: opaque, on top of the conversation until
// closed with Esc.
type btwOverlay struct {
	mu         sync.Mutex
	question   string
	answer     string
	processing bool
	errMsg     string
	scroll     int
	cancel     context.CancelFunc
}

func newBTWOverlay(question string) *btwOverlay {
	return &btwOverlay{
		question:   question,
		processing: true,
	}
}

func (o *btwOverlay) setCancel(c context.CancelFunc) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.cancel = c
}

func (o *btwOverlay) appendText(chunk string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.answer += chunk
}

func (o *btwOverlay) finish(err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.processing = false
	o.cancel = nil
	if err != nil {
		o.errMsg = err.Error()
	}
}

func (o *btwOverlay) snapshot() (question, answer, errMsg string, processing bool, scroll int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.question, o.answer, o.errMsg, o.processing, o.scroll
}

func (o *btwOverlay) render(scrollRows, cols int) (int, []string) {
	return o.renderWithFrame(scrollRows, cols, 0)
}

func (o *btwOverlay) renderWithFrame(scrollRows, cols, frame int) (int, []string) {
	question, answer, errMsg, processing, scroll := o.snapshot()
	panelRows := scrollRows
	if panelRows < 4 {
		panelRows = 4
	}
	if cols < 12 {
		cols = 12
	}
	bg := approvalPanelBG()
	mk := func(content string) string { return fillWidthBG(bg, content, cols) }
	blank := mk("")
	wrapW := cols - 4
	if wrapW < 8 {
		wrapW = 8
	}

	title := mk(fgCyan + bold + "  btw · side question" + reset)

	// Question header (may wrap).
	var head []string
	for i, ql := range wrapPlain(question, wrapW) {
		if i == 0 {
			head = append(head, mk("  "+dim+"Q: "+reset+ql))
		} else {
			head = append(head, mk("     "+ql))
		}
	}
	head = append(head, blank)

	// Answer body (scrollable).
	var full []string
	switch {
	case errMsg != "":
		for _, ln := range wrapPlain(errMsg, wrapW) {
			full = append(full, mk("  "+fgRed+bold+ln+reset))
		}
	case answer == "" && processing:
		full = append(full, mk("  "+dim+spinnerChar(frame)+" thinking…"+reset))
	default:
		for _, ln := range wrapPlain(answer, wrapW) {
			full = append(full, mk("  "+ln))
		}
		if processing {
			full = append(full, mk("  "+dim+spinnerChar(frame)+" …"+reset))
		}
	}

	// Body rows = panel minus title, footer, and the question header.
	bodyRows := panelRows - 2 - len(head)
	if bodyRows < 1 {
		bodyRows = 1
	}
	needsScroll := len(full) > bodyRows
	maxScroll := 0
	if needsScroll {
		maxScroll = len(full) - bodyRows
	}
	if scroll > maxScroll {
		scroll = maxScroll
	}
	if scroll < 0 {
		scroll = 0
	}
	o.mu.Lock()
	o.scroll = scroll // persist the clamp so ↓ can't run away
	o.mu.Unlock()
	body := full
	if needsScroll {
		body = full[scroll : scroll+bodyRows]
	}

	footerText := "  Esc close"
	switch {
	case processing:
		footerText = "  Esc cancel"
	case needsScroll:
		footerText = "  Esc close · ↑↓ scroll"
	}
	footer := mk(dim + footerText + reset)

	out := make([]string, panelRows)
	out[0] = title
	out[panelRows-1] = footer
	idx := 1
	for _, h := range head {
		if idx >= panelRows-1 {
			break
		}
		out[idx] = h
		idx++
	}
	for _, bd := range body {
		if idx >= panelRows-1 {
			break
		}
		out[idx] = bd
		idx++
	}
	for i := idx; i < panelRows-1; i++ {
		out[i] = blank
	}
	return 1, out
}

func (o *btwOverlay) feedKey(k Key) (handled bool, done bool, cancel bool) {
	switch {
	case k.isNavUp():
		o.mu.Lock()
		if o.scroll > 0 {
			o.scroll--
		}
		o.mu.Unlock()
		return true, false, false
	case k.isNavDown():
		o.mu.Lock()
		o.scroll++
		o.mu.Unlock()
		return true, false, false
	case k.Kind == KeyEscape:
		o.mu.Lock()
		proc := o.processing
		c := o.cancel
		o.mu.Unlock()
		if proc && c != nil {
			c()
		}
		return true, true, proc
	}
	return false, false, false
}

func wrapPlain(text string, width int) []string {
	if width < 1 {
		width = 1
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var out []string
	for _, para := range strings.Split(text, "\n") {
		if para == "" {
			out = append(out, "")
			continue
		}
		out = append(out, wrapLine(para, width)...)
	}
	return out
}