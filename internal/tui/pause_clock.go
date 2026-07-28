package tui

import (
	"sync"
	"time"
)

// approvalClock accounts for wall time the session spends parked at a human
// approval prompt. Tool cards, thinking blocks and subagent widgets all show
// elapsed time derived from their StartedAt, and an approval gate can hold a
// call for minutes while the human reads it — leaving those timers running
// reports "Bash (312.0s)" for a command that took 40ms once approved, which
// also poisons the tok/s and duration readings people use to spot slow tools.
//
// One process-wide clock is enough: a TUI is a single interactive session, and
// approvals are what stop the world in it. Blocks snapshot its total at
// creation (BlockMeta.PauseBaseMs) so only pause time overlapping a block's
// own lifetime is subtracted.
var approvalClock = &pauseClock{}

type pauseClock struct {
	mu    sync.Mutex
	accum time.Duration
	since time.Time // non-zero while paused
	depth int       // nested begin() calls sharing one window
}

// begin starts (or nests into) a pause. Nested begins share one window: the
// outermost end closes it, which is what happens when a /btw approval opens on
// top of a main-turn approval.
func (c *pauseClock) begin() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.depth++
	if c.depth == 1 {
		c.since = time.Now()
	}
}

// end closes the pause, banking its duration.
func (c *pauseClock) end() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.depth == 0 {
		return
	}
	c.depth--
	if c.depth == 0 && !c.since.IsZero() {
		c.accum += time.Since(c.since)
		c.since = time.Time{}
	}
}

// totalMs is all paused time so far, including an open pause window — so a
// timer read repeatedly while the human is still deciding stays frozen
// instead of jumping when they finally answer.
func (c *pauseClock) totalMs() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	total := c.accum
	if !c.since.IsZero() {
		total += time.Since(c.since)
	}
	return total.Milliseconds()
}

// markStarted stamps a block's start time together with the approval-pause
// baseline it must be measured against. Always set the pair through this — a
// bare StartedAt = time.Now() would silently subtract every earlier approval
// in the session from this block's elapsed time.
func markStarted(m *BlockMeta) {
	m.StartedAt = time.Now()
	m.PauseBaseMs = approvalClock.totalMs()
}

// blockElapsedMs is wall time since a block started, minus the approval time
// that fell inside its lifetime. Returns 0 for a block with no start time.
func blockElapsedMs(m BlockMeta) int64 {
	if m.StartedAt.IsZero() {
		return 0
	}
	elapsed := time.Since(m.StartedAt).Milliseconds() - (approvalClock.totalMs() - m.PauseBaseMs)
	if elapsed < 0 {
		return 0
	}
	return elapsed
}
