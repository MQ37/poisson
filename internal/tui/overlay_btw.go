package tui

import (
	"context"
	"strings"
	"sync"
)

// btwOverlay is a full-width floating box for side questions (/btw).
type btwOverlay struct {
	mu         sync.Mutex
	question   string
	answer     string
	processing bool
	errMsg     string
	scroll     int
	maxHeight  int
	cancel     context.CancelFunc
}

func newBTWOverlay(question string, maxHeight int) *btwOverlay {
	if maxHeight < 5 {
		maxHeight = 5
	}
	return &btwOverlay{
		question:   question,
		processing: true,
		maxHeight:  maxHeight,
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

func (o *btwOverlay) snapshot() (question, answer, errMsg string, processing bool, scroll, maxH int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.question, o.answer, o.errMsg, o.processing, o.scroll, o.maxHeight
}

func (o *btwOverlay) render(scrollRows, cols int) (int, []string) {
	return o.renderWithFrame(scrollRows, cols, 0)
}

func (o *btwOverlay) renderWithFrame(scrollRows, cols, frame int) (int, []string) {
	question, answer, errMsg, processing, scroll, maxH := o.snapshot()
	if maxH < 5 {
		maxH = 5
	}
	inner := boxInnerWidth(cols, cols-4)

	var body []string
	q := truncatePlain(question, inner-2)
	body = append(body, dim+"? "+reset+renderInline(q))

	if processing && answer == "" && errMsg == "" {
		body = append(body, dim+spinnerChar(frame)+" thinking…"+reset)
	} else if errMsg != "" {
		body = append(body, fgRed+bold+truncatePlain(errMsg, inner-2)+reset)
	} else {
		wrapped := wrapPlain(answer, inner-2)
		if len(wrapped) == 0 && processing {
			body = append(body, dim+spinnerChar(frame)+" thinking…"+reset)
		} else {
			body = append(body, wrapped...)
		}
	}

	maxBody := maxH - 4
	if maxBody < 1 {
		maxBody = 1
	}
	needsScroll := len(body) > maxBody
	footer := ""
	if processing {
		footer = dim + "Esc cancel" + reset
	} else if needsScroll {
		footer = dim + "Esc close · ↑↓ scroll" + reset
	} else {
		footer = dim + "Esc close" + reset
	}

	if len(body) > maxBody {
		if scroll > len(body)-maxBody {
			scroll = len(body) - maxBody
		}
		if scroll < 0 {
			scroll = 0
		}
		body = body[scroll : scroll+maxBody]
	} else {
		scroll = 0
	}

	var lines []string
	lines = append(lines, boxTopBorder("btw", inner))
	for _, ln := range body {
		lines = append(lines, boxBodyLine(inner, ln))
	}
	if footer != "" {
		lines = append(lines, boxBodyLine(inner, footer))
	}
	lines = append(lines, boxBottomBorder(inner))

	height := len(lines)
	// Lower-center: sit in the bottom half of the scroll region.
	anchor := scrollRows - height - 1
	lowerMid := scrollRows/2 + 1
	if anchor < lowerMid {
		anchor = lowerMid
	}
	if anchor < 1 {
		anchor = 1
	}
	if anchor+height-1 > scrollRows {
		anchor = scrollRows - height + 1
		if anchor < 1 {
			anchor = 1
		}
	}

	out := make([]string, len(lines))
	for i, ln := range lines {
		out[i] = truncateToWidth(ln, cols)
	}
	return anchor, out
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