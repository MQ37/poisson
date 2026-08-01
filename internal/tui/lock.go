package tui

// withLock runs fn while holding t.mu, guaranteeing release via defer even
// if fn panics. A bare t.mu.Lock()/t.mu.Unlock() pair (the pattern this
// replaces throughout the package) leaves t.mu stuck locked forever if a
// panic occurs in between — harmless when a panic anywhere crashes the
// whole process, but no longer true here: the input-loop and usage-refresh
// goroutines (see lifecycle.go) recover from panics instead of crashing,
// so a leftover locked t.mu would hang the render loop and the SIGINT
// handler's waitForAgentStop (both take t.mu) instead of just losing that
// one goroutine.
//
// Only use this for a critical section that ends with the lock released —
// i.e. nothing after fn() in the caller still needs to run under the lock,
// and fn() itself must never call anything that tries to re-lock t.mu
// (non-reentrant). Several call sites in this package deliberately unlock
// BEFORE calling further code that re-locks (e.g. handleOneMouseEvent
// unlocking before handleScrollDelta, which takes the lock itself) — those
// are restructured case by case, not blindly wrapped in withLock.
func (t *TUI) withLock(fn func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	fn()
}
