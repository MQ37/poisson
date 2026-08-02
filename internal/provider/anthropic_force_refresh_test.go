package provider

import (
	"testing"
	"time"

	"github.com/mq37/poisson/internal/auth"
	"github.com/mq37/poisson/internal/testutil"
)

// TestForceRefreshOAuth_NoAnthropicCredentialsError confirms forceRefreshOAuth
// is wired to the "anthropic" key specifically (not a typo, and not some
// other provider's key) — an empty auth store must fail with
// auth.ForceRefresh's own exact "no anthropic credentials" error.
func TestForceRefreshOAuth_NoAnthropicCredentialsError(t *testing.T) {
	p := NewAnthropicProvider(auth.AuthStore{}, nil)
	err := p.forceRefreshOAuth()
	if err == nil {
		t.Fatal("expected an error with no anthropic credentials")
	}
	if err.Error() != "no anthropic credentials" {
		t.Fatalf("err = %q, want exactly %q", err.Error(), "no anthropic credentials")
	}
}

// TestForceRefreshOAuth_BlocksOnStoreMu proves forceRefreshOAuth actually
// takes auth.StoreMu before doing anything else — the exact class of bug
// this session's real regression was (a refactor that dropped a shared lock
// call, letting two providers race the same shared AuthStore map; see
// auth.StoreMu's doc comment). To keep this network-free, the disk already
// holds a DIFFERENT "anthropic" entry than the caller's in-memory copy, so
// once unblocked, ForceRefresh's cross-process check (refresh_crossprocess.go)
// adopts that disk entry and returns WITHOUT ever calling
// auth.RefreshAnthropicToken — the lock is proven with zero real network
// calls.
func TestForceRefreshOAuth_BlocksOnStoreMu(t *testing.T) {
	testutil.TempHome(t)
	staleEntry := auth.AuthEntry{Type: "oauth", Access: "stale", Refresh: "r1", Expires: 1}
	if err := auth.Save(auth.AuthStore{"anthropic": {Type: "oauth", Access: "fresh-from-disk", Refresh: "r2", Expires: 1 << 62}}); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	p := NewAnthropicProvider(auth.AuthStore{"anthropic": staleEntry}, nil)

	auth.StoreMu.Lock()
	done := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		errCh <- p.forceRefreshOAuth()
		close(done)
	}()

	select {
	case <-done:
		auth.StoreMu.Unlock()
		t.Fatal("forceRefreshOAuth returned before auth.StoreMu was released — it isn't taking the lock")
	case <-time.After(150 * time.Millisecond):
		// Still blocked, as expected: forceRefreshOAuth is waiting on the
		// lock we hold.
	}
	auth.StoreMu.Unlock()

	select {
	case <-done:
		if err := <-errCh; err != nil {
			t.Fatalf("forceRefreshOAuth: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("forceRefreshOAuth never completed after auth.StoreMu was released")
	}
}
