package tui

import (
	"context"
	"strings"
	"sync"
)

// btwOverlay is a floating top-right box for side questions (/btw).
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
	if maxHeight < 3 {
		maxHeight = 3
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
	question, answer, errMsg, processing, scroll, maxH := o.snapshot()
	if maxH < 3 {
		maxH = 3
	}
	inner := cols / 2
	if inner > 52 {
		inner = 52
	}
	if inner < 20 {
		inner = cols - 4
		if inner < 12 {
			inner = 12
		}
	}

	var body []string
	q := truncatePlain(question, inner-2)
	body = append(body, dim+"? "+reset+renderInline(q))

	if processing && answer == "" && errMsg == "" {
		body = append(body, dim+spinnerChar(0)+" thinking…"+reset)
	} else if errMsg != "" {
		body = append(body, fgRed+bold+truncatePlain(errMsg, inner-2)+reset)
	} else {
		wrapped := wrapPlain(answer, inner-2)
		if len(wrapped) == 0 && processing {
			body = append(body, dim+spinnerChar(0)+" thinking…"+reset)
		} else {
			body = append(body, wrapped...)
		}
	}

	footer := ""
	if processing {
		footer = dim + "Esc cancel" + reset
	} else {
		footer = dim + "Esc close · ↑↓ scroll" + reset
	}

	// Reserve top, bottom, footer inside maxH.
	maxBody := maxH - 3
	if maxBody < 1 {
		maxBody = 1
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

	boxW := inner + 2
	title := "─ btw "
	fill := boxW - visibleWidth("╭"+title+"╮")
	if fill < 0 {
		fill = 0
	}
	top := fgGray + "╭" + title + strings.Repeat("─", fill) + "╮" + reset
	bot := fgGray + "╰" + strings.Repeat("─", inner) + "╯" + reset

	var lines []string
	lines = append(lines, alignRight(top, cols))
	for _, ln := range body {
		pad := inner - visibleWidth(ln)
		if pad < 0 {
			ln = truncateToWidth(ln, inner)
			pad = inner - visibleWidth(ln)
		}
		if pad < 0 {
			pad = 0
		}
		row := fgGray + "│" + reset + " " + ln + strings.Repeat(" ", pad) + " " + fgGray + "│" + reset
		lines = append(lines, alignRight(row, cols))
	}
	if footer != "" {
		pad := inner - visibleWidth(footer)
		if pad < 0 {
			pad = 0
		}
		row := fgGray + "│" + reset + " " + footer + strings.Repeat(" ", pad) + " " + fgGray + "│" + reset
		lines = append(lines, alignRight(row, cols))
	}
	lines = append(lines, alignRight(bot, cols))
	return 1, lines
}

func (o *btwOverlay) feedKey(data []byte) (handled bool, done bool, cancel bool) {
	if isArrowUp(data) {
		o.mu.Lock()
		if o.scroll > 0 {
			o.scroll--
		}
		o.mu.Unlock()
		return true, false, false
	}
	if isArrowDown(data) {
		o.mu.Lock()
		o.scroll++
		o.mu.Unlock()
		return true, false, false
	}
	for _, b := range data {
		if b == 27 && !hasCSI(data) {
			o.mu.Lock()
			proc := o.processing
			c := o.cancel
			o.mu.Unlock()
			if proc && c != nil {
				c()
			}
			return true, true, proc
		}
	}
	return false, false, false
}

func alignRight(line string, cols int) string {
	w := visibleWidth(line)
	if w >= cols {
		return truncateToWidth(line, cols)
	}
	return strings.Repeat(" ", cols-w) + line
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