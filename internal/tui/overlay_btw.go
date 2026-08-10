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
	// status is a short "tool(arg)" description of the read-only tool call
	// currently in flight (see Agent.StreamQuickAnswer's onToolStatus), shown
	// next to the spinner instead of a bare "thinking…"/"…". Cleared the
	// moment real answer text starts arriving, so it never shows a stale tool
	// name once the model has moved on to producing its final answer.
	status string
	// closed is closed exactly once when this panel actually goes away (see
	// cancelOverlayWork, the only place that ever tears a live /btw stream
	// down). A bash approval originating from the main conversation (or a
	// subagent) while /btw's own panel is showing parks behind it instead of
	// destroying it — tui.TUI.Approve waits on this channel before it ever
	// shows its own prompt, so /btw stays alive and in front until the user
	// closes it themselves.
	closed    chan struct{}
	closeOnce sync.Once
}

func newBTWOverlay(question string) *btwOverlay {
	return &btwOverlay{
		question:   question,
		processing: true,
		closed:     make(chan struct{}),
	}
}

// closedCh returns the signal channel a parked non-btw approval waits on.
func (o *btwOverlay) closedCh() <-chan struct{} { return o.closed }

// markClosed signals closedCh exactly once. Safe to call more than once or
// concurrently.
func (o *btwOverlay) markClosed() { o.closeOnce.Do(func() { close(o.closed) }) }

func (o *btwOverlay) setCancel(c context.CancelFunc) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.cancel = c
}

func (o *btwOverlay) appendText(chunk string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.answer += chunk
	o.status = ""
}

func (o *btwOverlay) setStatus(text string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.status = text
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

func (o *btwOverlay) snapshot() (question, answer, errMsg string, processing bool, scroll int, status string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.question, o.answer, o.errMsg, o.processing, o.scroll, o.status
}

func (o *btwOverlay) render(scrollRows, cols int) (int, []string) {
	return o.renderWithFrame(scrollRows, cols, 0)
}

func (o *btwOverlay) renderWithFrame(scrollRows, cols, frame int) (int, []string) {
	question, answer, errMsg, processing, scroll, status := o.snapshot()
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
		label := "thinking…"
		if status != "" {
			label = status
		}
		full = append(full, mk("  "+dim+spinnerChar(frame)+" "+label+reset))
	default:
		// Same renderer the main conversation uses for assistant text — bold,
		// inline code, and fenced code blocks all render identically here
		// instead of /btw showing a plain word-wrapped dump of the same answer.
		for _, ln := range layoutRichMarkdown(answer, wrapW, "") {
			// A reset anywhere inside the line (every styled span ends with one)
			// would otherwise drop back to the terminal's default background
			// instead of the panel's — reapply it after every reset, not just
			// the one at the end that mk() already covers.
			full = append(full, mk("  "+strings.ReplaceAll(ln, reset, reset+bg)))
		}
		if processing {
			label := "…"
			if status != "" {
				label = status
			}
			full = append(full, mk("  "+dim+spinnerChar(frame)+" "+label+reset))
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

// scrollBy adjusts the answer scroll position by delta (positive = later
// content, matching isNavDown's o.scroll++ convention) — the mouse-wheel
// counterpart to feedKey's arrow-key handling. render's own clamp on the
// next paint keeps it in [0, maxScroll]; the floor here just keeps a large
// negative wheel burst from needing that clamp to be re-derived here too.
func (o *btwOverlay) scrollBy(delta int) {
	o.mu.Lock()
	o.scroll += delta
	if o.scroll < 0 {
		o.scroll = 0
	}
	o.mu.Unlock()
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
