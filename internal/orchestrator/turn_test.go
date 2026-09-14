package orchestrator

import (
	"testing"
	"time"
)

// TestShouldFlushText_MinCharsGatesTheIntervalPath is the regression guard
// for the live-observed bug: a lone stray character (e.g. "u") must never
// be flushed on its own just because turnBatchInterval elapsed — only once
// there's turnBatchMinChars worth of something to actually send.
func TestShouldFlushText_MinCharsGatesTheIntervalPath(t *testing.T) {
	cases := []struct {
		name     string
		batchLen int
		elapsed  time.Duration
		want     bool
	}{
		{"one char, interval elapsed: never flush alone", 1, turnBatchInterval + time.Second, false},
		{"below min chars, interval elapsed: still withheld", turnBatchMinChars - 1, turnBatchInterval * 2, false},
		{"at min chars, interval elapsed: flush", turnBatchMinChars, turnBatchInterval, true},
		{"well above min chars, interval elapsed: flush", turnBatchMinChars * 3, turnBatchInterval, true},
		{"at min chars, interval NOT elapsed: withheld", turnBatchMinChars, turnBatchInterval - time.Millisecond, false},
		{"max chars reached, interval not elapsed: flush anyway (size path)", turnBatchMaxChars, 0, true},
		{"one char, interval not elapsed: withheld", 1, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldFlushText(c.batchLen, c.elapsed); got != c.want {
				t.Errorf("shouldFlushText(%d, %v) = %v, want %v", c.batchLen, c.elapsed, got, c.want)
			}
		})
	}
}
