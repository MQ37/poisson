package tui

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

func TestDedupName(t *testing.T) {
	cases := map[string]string{
		"compute-2plus2 compute-2plus2": "compute-2plus2",
		"calc-subagent calc-subagent":   "calc-subagent",
		"code reviewer":                 "code reviewer",
		"explore":                       "explore",
		"":                              "",
		"a a a":                         "a",
	}
	for in, want := range cases {
		if got := dedupAdjacentWords(in); got != want {
			t.Errorf("dedupAdjacentWords(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestAnimateSpinnerInLineNoDup guards against the spinner substitution
// duplicating visible content on lines whose ANSI codes are interspersed
// (not a single leading prefix), such as the subagent widget.
func TestAnimateSpinnerInLineNoDup(t *testing.T) {
	// Interspersed styling: dim spinner, cyan+bold name, dim timer.
	line := dim + toolCardSpinnerSlot + reset + " " + fgCyan + bold + "worker" + reset + "  " + dim + "0.0s" + reset
	got := stripANSI(animateSpinnerInLine(line, 0))
	if strings.Count(got, "worker") != 1 {
		t.Errorf("name duplicated after animation: %q", got)
	}
	if strings.Contains(got, toolCardSpinnerSlot) {
		t.Errorf("spinner slot not replaced: %q", got)
	}
	if !strings.Contains(got, "worker  0.0s") {
		t.Errorf("content mangled: %q", got)
	}

	// Simple single-prefix (tool-card style) still animates once.
	tc := fgYellow + "╭─ bash ──" + toolCardSpinnerSlot + "─╮" + reset
	gotTC := stripANSI(animateSpinnerInLine(tc, 3))
	if strings.Contains(gotTC, toolCardSpinnerSlot) {
		t.Errorf("tool-card spinner not replaced: %q", gotTC)
	}
	if strings.Count(gotTC, "bash") != 1 {
		t.Errorf("tool-card content duplicated: %q", gotTC)
	}
}

// TestSubagentWidgetRendersNameOnce is a full-paint regression: a running
// subagent widget must show its name exactly once on screen.
func TestSubagentWidgetRendersNameOnce(t *testing.T) {
	e := newTUIIntegEnv(t, nil)
	e.tui.cols = 120
	e.tui.rows = 30
	e.tui.mu.Lock()
	e.tui.scroll.appendSubagentCard(1, "c1", "worker", "Compute two plus two", "ollama/glm-5.2:cloud")
	e.tui.status.Thinking = true
	e.tui.mu.Unlock()

	e.tui.recomputeLayout()
	e.tui.dirty.markFull()
	e.tui.paint(e.tui.dirty.consume())
	out := stripANSI(e.tui.writer.(*bytes.Buffer).String())

	if n := strings.Count(out, "worker"); n != 1 {
		t.Errorf("subagent name rendered %d times, want 1", n)
	}

	// Verify it's the pinned widget line (spinner + name + timer).
	cup := regexp.MustCompile(`\x1b\[(\d+);(\d+)H`)
	full := e.tui.writer.(*bytes.Buffer).String()
	locs := cup.FindAllStringSubmatchIndex(full, -1)
	var widgetLine string
	for i, loc := range locs {
		start := loc[1]
		end := len(full)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		content := strings.TrimRight(stripANSI(full[start:end]), " ")
		if strings.Contains(content, "worker") {
			widgetLine = content
		}
	}
	if !strings.Contains(widgetLine, "0.0s") {
		t.Errorf("widget line missing timer: %q", widgetLine)
	}
}
