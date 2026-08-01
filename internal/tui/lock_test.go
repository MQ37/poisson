package tui

import "testing"

// TestWithLockReleasesOnPanic is the regression guard for the whole
// withLock refactor: a panic inside the locked function must still release
// t.mu (via defer), not leave it stuck locked forever — which is exactly
// what the old bare Lock()/Unlock() pattern this package used everywhere
// would do, and now matters in practice since the input-loop and
// usage-refresh goroutines (lifecycle.go) recover from panics instead of
// crashing the whole process.
func TestWithLockReleasesOnPanic(t *testing.T) {
	tu := &TUI{}

	func() {
		defer func() { recover() }()
		tu.withLock(func() { panic("boom") })
	}()

	if !tu.mu.TryLock() {
		t.Fatal("t.mu still locked after a panic inside withLock — defer didn't run")
	}
	tu.mu.Unlock()
}

// TestWithLockRunsFnUnderLock confirms the normal (non-panic) path still
// actually holds the lock while fn runs — withLock must not be a no-op.
func TestWithLockRunsFnUnderLock(t *testing.T) {
	tu := &TUI{}
	ran := false
	tu.withLock(func() {
		ran = true
		if tu.mu.TryLock() {
			tu.mu.Unlock()
			t.Fatal("t.mu was not held while fn ran")
		}
	})
	if !ran {
		t.Fatal("fn never ran")
	}
	if !tu.mu.TryLock() {
		t.Fatal("t.mu still locked after withLock returned")
	}
	tu.mu.Unlock()
}
