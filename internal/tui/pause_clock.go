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
// creation (BlockMeta.PauseBase) so only pause time overlapping a block's
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

// total is all paused time so far, including an open pause window — so a
// timer read repeatedly while the human is still deciding stays frozen
// instead of jumping when they finally answer. now is caller-supplied
// (rather than an internal time.Now()) so blockElapsedMs can measure both
// halves of its subtraction against the exact same instant — see its doc
// comment. Returns a full-precision Duration, not milliseconds: rounding to
// ms here, before blockElapsedMs's own subtraction, is exactly the bug that
// method's comment describes.
func (c *pauseClock) total(now time.Time) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	total := c.accum
	if !c.since.IsZero() {
		total += now.Sub(c.since)
	}
	return total
}

// totalMs is total(time.Now()) truncated to milliseconds, for a caller that
// only needs a coarse "how long has this session spent waiting on approvals
// so far" figure (there is currently no such caller outside tests, but this
// stays as the natural public unit for one — every other reader wants the
// full-precision Duration).
func (c *pauseClock) totalMs() int64 {
	return c.total(time.Now()).Milliseconds()
}

// markStarted stamps a block's start time together with the approval-pause
// baseline it must be measured against. Always set the pair through this — a
// bare StartedAt = time.Now() would silently subtract every earlier approval
// in the session from this block's elapsed time.
func markStarted(m *BlockMeta) {
	m.StartedAt = time.Now()
	m.PauseBase = approvalClock.total(m.StartedAt)
}

// blockElapsedMs is wall time since a block started, minus the approval time
// that fell inside its lifetime. Returns 0 for a block with no start time.
//
// Everything here stays a time.Duration (nanosecond precision) until the
// final .Milliseconds() call — never round to milliseconds and then
// subtract. An earlier version rounded both operands independently
// (time.Since(...).Milliseconds() minus approvalClock's own already-ms
// total) before subtracting: while frozen at an approval prompt, the two
// values grow at exactly the same real rate and are meant to cancel out to
// a constant, but each was floor-truncated to milliseconds against a
// different reference instant (m.StartedAt vs the pause's own start), so
// their difference wasn't actually constant — it stepped between two
// adjacent integer-ms values every time real elapsed time crossed either
// operand's own millisecond boundary, at a rate having nothing to do with
// render ticks. For a command whose real pre-approval gap is itself
// sub-millisecond (the guard fast path or an already-warm classifier flags
// an obviously dangerous command almost instantly — a common case), that
// step sits right at the ms=0/ms=1 line, so the value alternated between 0
// and 1 as the approval prompt just sat there — and formatToolCollapsed
// hides its "(0.0s)" suffix entirely at exactly ms=0, so the suffix
// visibly flickered in and out every render tick while a human read the
// prompt. Working entirely in Duration and truncating only once
// eliminates the double-rounding: since.Sub(StartedAt) (the true frozen
// gap) is then computed exactly, so its single truncation to ms is a fixed
// value for as long as the pause stays open.
func blockElapsedMs(m BlockMeta) int64 {
	if m.StartedAt.IsZero() {
		return 0
	}
	now := time.Now()
	elapsed := now.Sub(m.StartedAt) - (approvalClock.total(now) - m.PauseBase)
	if elapsed < 0 {
		return 0
	}
	return elapsed.Milliseconds()
}
